// Package httpcodec_test содержит модульные тесты для проверки корректности работы сетевого кодека.
package httpcodec_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/ioncode/httpcodec"
)

// Тестовая DTO-структура для проверки маршалинга.
type samplePayload struct {
	User  string `json:"user"`
	Score int    `json:"score"`
}

// TestCodec_ReadBytes_Success проверяет корректность вычитки сырых текстовых данных.
func TestCodec_ReadBytes_Success(t *testing.T) {
	codec := httpcodec.New(1024)
	expectedText := "https://yandex.ru"

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(expectedText))
	res := httptest.NewRecorder()

	var executed bool
	ok := codec.ReadBytes(res, req, func(payload []byte) bool {
		executed = true
		if string(payload) != expectedText {
			t.Errorf("Ожидался текст %q, получен %q", expectedText, string(payload))
		}
		return true
	})

	if !ok {
		t.Fatal("ReadBytes вернул false, хотя запрос был валидным")
	}
	if !executed {
		t.Fatal("Функция-коллбэк процессора не была вызвана")
	}
}

// TestCodec_ReadJSON_Success проверяет штатное декодирование валидного JSON-объекта.
func TestCodec_ReadJSON_Success(t *testing.T) {
	codec := httpcodec.New(1024)
	jsonBody := `{"user":"highload_dev","score":100}`

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(jsonBody))
	res := httptest.NewRecorder()

	var dst samplePayload
	ok := codec.ReadJSON(res, req, &dst)

	if !ok {
		t.Fatal("ReadJSON вернул false для валидного JSON")
	}
	if dst.User != "highload_dev" || dst.Score != 100 {
		t.Errorf("Данные десериализованы некорректно: %+v", dst)
	}
}

// TestCodec_ReadJSON_Invalid проверяет, что при битом JSON кодек возвращает 400 Bad Request.
func TestCodec_ReadJSON_Invalid(t *testing.T) {
	codec := httpcodec.New(1024)
	brokenJSON := `{"user":"highload_dev", broken_json_here}`

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(brokenJSON))
	res := httptest.NewRecorder()

	var dst samplePayload
	ok := codec.ReadJSON(res, req, &dst)

	if ok {
		t.Fatal("ReadJSON вернул true для поврежденного JSON-пакета")
	}
	if res.Code != http.StatusBadRequest {
		t.Errorf("Ожидался статус-код 400 Bad Request, получен %d", res.Code)
	}
}

// TestCodec_WriteJSON_Success проверяет потоковую отправку ответа с правильными заголовками.
func TestCodec_WriteJSON_Success(t *testing.T) {
	codec := httpcodec.New(1024)
	data := samplePayload{User: "ioncode", Score: 42}
	res := httptest.NewRecorder()

	codec.WriteJSON(res, http.StatusCreated, &data)

	if res.Code != http.StatusCreated {
		t.Errorf("Ожидался статус 201 Created, получен %d", res.Code)
	}

	contentType := res.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Ожидался Content-Type 'application/json', получен %q", contentType)
	}

	var result samplePayload
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatalf("Тело ответа не является валидным JSON: %v", err)
	}

	if result.User != "ioncode" || result.Score != 42 {
		t.Errorf("Отправленные данные повредились при маршалинге: %+v", result)
	}
}

// TestCodec_Middleware_DoSProtection проверяет корректность работы контура
// аппаратной DoS-защиты сетевого периметра сокетов.
//
// Тест передает валидную JSON-строку, размер которой заведомо превышает
// установленный в кодеке лимит. Благодаря преаллокации буфера на maxBodySize + 1,
// метод ReadBytes гарантированно заглядывает за границу дозволенного объема данных.
//
// В результате стандартный лимитер http.MaxBytesReader выбрасывает аппаратную ошибку
// переполнения сокета, кодек прерывает обработку до этапа десериализации
// и возвращает клиенту законный статус-код 413 Request Entity Too Large.
func TestCodec_Middleware_DoSProtection(t *testing.T) {
	// Инициализируем кодек с маленьким лимитом в 15 байт
	const limit int64 = 15
	codec := httpcodec.New(limit)

	// Создаем тестовый хендлер, который пытается прочесть и распарсить JSON
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var dst samplePayload
		_ = codec.ReadJSON(w, r, &dst)
	})

	// Извлекаем функцию-перехватчик и оборачиваем в неё наш хендлер
	mw := codec.Middleware()
	routerWithMiddleware := mw(testHandler)

	// Передаем валидный, но заведомо ДЛИННЫЙ JSON (46 байт, что значительно больше лимита 15)
	heavyJSON := `{"user":"highload_developer_name","score":100}`

	// Используем стандартный strings.NewReader без усложнения пайпами
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(heavyJSON))
	res := httptest.NewRecorder()

	// Симулируем прохождение запроса через Middleware веб-сервера
	routerWithMiddleware.ServeHTTP(res, req)

	// Теперь, благодаря двойному контуру защиты кодека (проверка n > c.maxBodySize
	// и отслеживание *http.MaxBytesError), мы гарантированно получаем статус 413
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf(
			"DoS защита не сработала. Ожидался статус 413 Request Entity Too Large, получен %d. Ответ: %s",
			res.Code,
			res.Body.String(),
		)
	}
}

// защита от DoS на уровне кодека при забытом подключении middleware
func TestCodec_DoSProtection(t *testing.T) {
	// Инициализируем кодек с маленьким лимитом в 15 байт
	const limit int64 = 15
	codec := httpcodec.New(limit)

	// Создаем тестовый хендлер, который пытается прочесть и распарсить JSON
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var dst samplePayload
		_ = codec.ReadJSON(w, r, &dst)
	})

	// Передаем валидный, но заведомо ДЛИННЫЙ JSON (46 байт, что значительно больше лимита 15)
	heavyJSON := `{"user":"highload_developer_name","score":100}`

	// Используем стандартный strings.NewReader без усложнения пайпами
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(heavyJSON))
	res := httptest.NewRecorder()

	// Симулируем прохождение запроса через Middleware веб-сервера
	testHandler.ServeHTTP(res, req)

	// Теперь, благодаря двойному контуру защиты кодека (проверка n > c.maxBodySize
	// и отслеживание *http.MaxBytesError), мы гарантированно получаем статус 413
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Errorf(
			"DoS защита не сработала. Ожидался статус 413 Request Entity Too Large, получен %d. Ответ: %s",
			res.Code,
			res.Body.String(),
		)
	}
}
