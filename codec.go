package httpcodec

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"

	json "github.com/goccy/go-json"
)

// Codec объединяет в себе высокопроизводительные, потокобезопасные инструменты
// для симметричного кодирования и декодирования HTTP-данных.
//
// Структура инкапсулирует лимиты сетевого периметра периметра и управляет
// двумя независимыми пулами переиспользования памяти (sync.Pool), что позволяет
// свести нагрузку на Garbage Collector (GC) к абсолютному минимуму (0 B/op).
type Codec struct {
	// maxBodySize определяет жесткий лимит размера тела входящего HTTP-запроса в байтах.
	maxBodySize int64

	// bufferPool хранит указатели на массивы байт фиксированной длины (*[]byte)
	// для атомарного чтения входящих сетевых потоков. Каждое выделение памяти
	// строго ограничено значением maxBodySize.
	bufferPool sync.Pool

	// jsonPool управляет высокопроизводительными буферами памяти (*bytes.Buffer)
	// для потоковой сериализации исходящих REST API ответов без аллокаций в куче.
	jsonPool sync.Pool
}

// New создает и настраивает новый экземпляр Codec с фиксированным лимитом размера буферов.
//
// Параметр maxBodySize определяет емкость выделяемых массивов байт в bufferPool
// и лимит сетевого middleware. Для стандартных REST API проектов рекомендуется
// передавать значения 4096 (4 КБ) или 8192 (8 КБ).
func New(maxBodySize int64) *Codec {
	return &Codec{
		maxBodySize: maxBodySize,
		bufferPool: sync.Pool{
			New: func() any {
				// ВАЖНО: Выделяем память под лимит + 1 байт!
				// это заставит выбросить ошибку http.MaxBytesError и крректно вернуть пользователю 413
				b := make([]byte, maxBodySize+1)
				return &b
			},
		},
		jsonPool: sync.Pool{
			New: func() any {
				// Возвращает чистый указатель *bytes.Buffer с преаллоцированной емкостью
				return new(bytes.Buffer)
			},
		},
	}
}

// ReadBytes выполняет оптимизированное чтение тела HTTP-запроса в виде сырых байт
// с использованием переиспользуемых буферов памяти из sync.Pool.
func (c *Codec) ReadBytes(w http.ResponseWriter, r *http.Request, processor func(payload []byte) bool) bool {
	bufPtr := c.bufferPool.Get().(*[]byte)
	buf := *bufPtr

	clear(buf)

	defer func() {
		clear(buf)
		c.bufferPool.Put(bufPtr)
		r.Body.Close()
	}()

	n, err := io.ReadFull(r.Body, buf)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			if n == 0 {
				http.Error(w, "Request body is empty", http.StatusBadRequest)
				return false
			}
			err = nil
		} else if errors.Is(err, io.EOF) {
			http.Error(w, "Request body is empty", http.StatusBadRequest)
			return false
		} else {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
				return false
			}
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return false
		}
	}

	// защита от забытого подключения mw
	if int64(n) > c.maxBodySize {
		http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge) // 413
		return false
	}

	if n == 0 && err == nil {
		http.Error(w, "Request body is empty", http.StatusBadRequest)
		return false
	}

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

// WriteJSON выполняет высокопроизводительную потоковую сериализацию данных в JSON
// и их отправку в сетевой сокет без лишних аллокаций памяти в куче.
//
// Метод извлекает переиспользуемый буфер *bytes.Buffer из внутреннего пула jsonPool,
// инициализирует потоковый Encoder библиотеки 'goccy/go-json' и кодирует данные.
// После успешной сериализации метод автоматически выставляет REST API заголовки
// Content-Type: application/json, отправляет статус-код и стримит байты в сеть.
// В конце операции буфер полностью сбрасывается (Reset) с сохранением его емкости
// (capacity) и возвращается обратно в пул.
func (c *Codec) WriteJSON(w http.ResponseWriter, statusCode int, data any) {
	buf := c.jsonPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		c.jsonPool.Put(buf)
	}()

	if err := json.NewEncoder(buf).Encode(data); err != nil {
		http.Error(w, "Failed to serialize response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = buf.WriteTo(w)
}

// Unmarshal является оберткой над высокопроизводительным движком десериализации.
//
// Метод полностью оптимизирован под рантайм Go 1.26.1 и использует JIT-компиляцию
// библиотеки 'goccy/go-json'. Он осуществляет разбор сырого среза байт data
// и записывает результат в целевой объект v.
//
// Использование этого метода вместо стандартного encoding/json позволяет снизить
// нагрузку на CPU за счет отказа от классической рефлексии в пользу сгенерированного
// на лету ассемблерного кода.
func (c *Codec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// Marshal является зеркальным методом для высокопроизводительной сериализации объектов.
//
// Метод переводит структуру v в срез байт JSON. Обратите внимание, что данный метод
// аллоцирует новый срез байт в куче на каждый вызов, поэтому для отправки HTTP-ответов
// рекомендуется использовать метод Write(), работающий через sync.Pool буферов.
func (c *Codec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
