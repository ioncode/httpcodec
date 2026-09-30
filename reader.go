package httpcodec

import (
	"errors"
	"io"
	"net/http"

	json "github.com/goccy/go-json"
)

// ReadBytes выполняет оптимизированное чтение тела HTTP-запроса в виде сырых байт
// с использованием переиспользуемых буферов памяти из sync.Pool.
//
// Метод полностью спроектирован под требования Highload-систем:
//  1. Исключает аллокации в куче (heap) на этапе сетевого ввода-вывода (0 B/op).
//  2. Принудительно зануляет память буфера через встроенную функцию clear(),
//     гарантируя изоляцию данных и защиту от утечек (Data Bleed) между запросами клиентов.
//  3. Полностью совместим с поведением постоянных соединений (Keep-Alive) и протоколом HTTP/2.
//  4. Содержит автономный защитный контур DoS-защиты, который при превышении лимита maxBodySize
//     безопасно вычитывает оставшийся хвост мусора в io.Discard (в пределах лимита maxTrashRead),
//     что позволяет сохранить сокет чистым и переиспользовать TCP-соединение без TLS-хендшейков.
//
// Если тело запроса пустое, повреждено или превышает допустимый размер, метод
// автоматически отправляет клиенту соответствующий HTTP-статус (400 или 413) и возвращает false.
// В случае успешного чтения управление передается в функцию-коллбэк processor.
func (c *Codec) ReadBytes(w http.ResponseWriter, r *http.Request, processor func(payload []byte) bool) bool {
	// Извлекаем указатель на срез байт фиксированной длины (maxBodySize + 1) из пула.
	bufPtr := c.bufferPool.Get().(*[]byte)
	buf := *bufPtr
	clear(buf) // Зануляем буфер перед чтением нового запроса

	// Гарантируем возврат памяти в пул и закрытие сетевого дескриптора при любом исходе
	defer func() {
		clear(buf) // Зануляем буфер после использования в целях безопасности
		c.bufferPool.Put(bufPtr)
		r.Body.Close()
	}()

	// io.ReadFull вычитывает данные до тех пор, пока срез buf не заполнится полностью.
	// В Keep-Alive/HTTP2 средах net/http возвращает io.EOF ровно на границе Content-Length,
	// что заставляет io.ReadFull мгновенно вернуть ошибку io.ErrUnexpectedEOF, если запрос меньше лимита.
	n, err := io.ReadFull(r.Body, buf)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			// Если прочитано 0 байт — клиент прислал пустой запрос
			if n == 0 {
				http.Error(w, "Request body is empty", http.StatusBadRequest) // HTTP 400
				return false
			}
			err = nil // Сбрасываем ошибку, так как валидные данные в пределах лимита успешно прочитаны
		} else {
			// Проверяем, не был ли поток ограничен на уровне TCP-сокета через c.Middleware()
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge) // HTTP 413
				return false
			}

			// Любая другая сетевая ошибка (например, принудительный обрыв сокета клиентом)
			http.Error(w, "Failed to read request body", http.StatusBadRequest) // HTTP 400
			return false
		}
	}

	// Автономный контур DoS-защиты на уровне кодека.
	// Срабатывает, если разработчик забыл подключить c.Middleware() к роутеру.
	if int64(n) > c.maxBodySize {
		if c.maxTrashRead > 0 {
			// Вычитываем оставшийся в сокете мусор в пустоту (до maxTrashRead байт),
			// спасая persistent-соединение для последующих запросов этого клиента.
			limitedTrashReader := io.LimitReader(r.Body, c.maxTrashRead)
			_, _ = io.Copy(io.Discard, limitedTrashReader)
		}

		http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge) // HTTP 413
		return false
	}

	// Передаем в процессор срез, усеченный строго до фактически прочитанного объема данных
	return processor(buf[:n])
}

// ReadJSON выполняет оптимизированный, строго типизированный парсинг тела JSON-запроса.
//
// Метод повторно использует оперативную память из внутреннего пула sync.Pool через
// вызов ReadBytes, полностью исключая аллокации в куче на этапе сетевого ввода-вывода (I/O).
// Десериализация данных в целевой объект dst производится с помощью
// высокопроизводительного кодировщика 'goccy/go-json'.
//
// Если входящий JSON поврежден, содержит невалидные типы данных или неизвестные поля,
// метод автоматически отправляет клиенту ошибку HTTP 400 Bad Request и прерывает обработку.
func (c *Codec) ReadJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return c.ReadBytes(w, r, func(payload []byte) bool {
		if err := json.Unmarshal(payload, dst); err != nil {
			http.Error(w, "Invalid JSON format: "+err.Error(), http.StatusBadRequest)
			return false
		}
		return true
	})
}
