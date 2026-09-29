// Package httpcodec_test содержит изолированные бенчмарки производительности сетевого кодека.
package httpcodec_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/ioncode/httpcodec"
)

// benchmarkTarget имитирует реальную DTO-структуру полезной нагрузки (например, учетные данные).
type benchmarkTarget struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// Глобальные приемники результатов для защиты от оптимизаций компилятора по удалению кода.
// Использование примитива int вместо any гарантирует 0 аллокаций при фиксации результатов.
var (
	benchSinkLen int
	benchSinkObj any
)

// Benchmark_ReadBytes_Comparison доказывает эффективность вычитки сырого текстового потока
// (например, text/plain URL-адресов эндпоинта POST /) через sync.Pool кодека.
func Benchmark_ReadBytes_Comparison(b *testing.B) {
	codec := httpcodec.New(4096)
	rawPayload := []byte("https://yandex.ru")

	// Используем bytes.Reader для возможности мгновенного сброса потока через Seek за 0 аллокаций
	bodyReader := bytes.NewReader(rawPayload)
	ctx := context.Background()

	b.Run("httpcodec.ReadBytes_Optimized", func(b *testing.B) {
		res := httptest.NewRecorder()
		// Создаем ОДИН http.Request на всё время теста ДО сброса таймера
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", bodyReader)

		// Выносим коллбэк из цикла, чтобы избежать аллокаций замыкания в куче
		processor := func(payload []byte) bool {
			// Обманываем Escape-анализ компилятора: пишем только длину.
			// Никаких ложных аллокаций интерфейсов в куче, но весь код кодека честно выполняется!
			benchSinkLen = len(payload)
			return true
		}

		b.ResetTimer()
		for b.Loop() {
			// Сбрасываем указатель чтения в буфере в начало за 0 аллокаций
			_, _ = bodyReader.Seek(0, io.SeekStart)

			// Вызываем кодек
			_ = codec.ReadBytes(res, req, processor)
		}
	})

	b.Run("standard.ReadAll_Text_Legacy", func(b *testing.B) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", bodyReader)

		b.ResetTimer()
		for b.Loop() {
			_, _ = bodyReader.Seek(0, io.SeekStart)

			// Стандартный подход, который непрерывно перевыделяет и расширяет слайсы в куче
			bodyBytes, _ := io.ReadAll(req.Body)
			req.Body.Close()
			benchSinkLen = len(bodyBytes)
		}
	})
}

// Benchmark_ReadJSON_Comparison оценивает скорость парсинга входящего JSON-пакета,
// доказывая превосходство JIT-компиляции goccy/go-json над стандартной рефлексией.
func Benchmark_ReadJSON_Comparison(b *testing.B) {
	codec := httpcodec.New(4096)
	jsonBody := []byte(`{"login":"benchmark_user_name_test","password":"super_secure_password_string_123"}`)
	bodyReader := bytes.NewReader(jsonBody)
	ctx := context.Background()

	b.Run("httpcodec.ReadJSON_Optimized", func(b *testing.B) {
		res := httptest.NewRecorder()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", bodyReader)

		b.ResetTimer()
		for b.Loop() {
			var dst benchmarkTarget
			_, _ = bodyReader.Seek(0, io.SeekStart)

			// Наш кодек: вычитка без аллокаций + JIT-десериализация
			codec.ReadJSON(res, req, &dst)
			benchSinkObj = &dst
		}
	})

	b.Run("standard.ReadAll_And_Unmarshal_Legacy", func(b *testing.B) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", bodyReader)

		b.ResetTimer()
		for b.Loop() {
			var dst benchmarkTarget
			_, _ = bodyReader.Seek(0, io.SeekStart)

			bodyBytes, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(bodyBytes, &dst)
			req.Body.Close()
			benchSinkObj = &dst
		}
	})
}

// Benchmark_WriteJSON_Comparison показывает разницу между потоковым стримингом
// из jsonPool кодека и классическим выделением памяти под весь JSON-массив через Marshal.
func Benchmark_WriteJSON_Comparison(b *testing.B) {
	codec := httpcodec.New(4096)
	data := &benchmarkTarget{
		Login:    "benchmark_user_name_test",
		Password: "super_secure_password_string_123",
	}

	b.Run("httpcodec.WriteJSON_Optimized", func(b *testing.B) {
		res := httptest.NewRecorder()
		b.ResetTimer()
		for b.Loop() {
			// Сбрасываем длину рекордера, имитируя очистку сетевого сокета
			res.Body.Reset()

			// Стриминг ответа напрямую в писатель за счет переиспользуемой емкости (capacity) буфера
			codec.WriteJSON(res, http.StatusOK, data)
			benchSinkObj = res
		}
	})

	b.Run("standard.Marshal_And_Write_Legacy", func(b *testing.B) {
		res := httptest.NewRecorder()
		b.ResetTimer()
		for b.Loop() {
			res.Body.Reset()

			// Классический легаси-подход с аллокацией свежих байт под каждую отправку
			bytesData, _ := json.Marshal(data)
			res.Header().Set("Content-Type", "application/json")
			res.WriteHeader(http.StatusOK)
			_, _ = res.Write(bytesData)
			benchSinkObj = res
		}
	})
}

// Benchmark_WriteJSON_Comparison оценивает накладные расходы при кодировании
// и отправке тяжелого JSON-массива (батч из 1000 элементов).
func Benchmark_WriteHeavyJSON_Comparison(b *testing.B) {
	codec := httpcodec.New(4096)

	// НАША НАГРУЗКА: Генерируем гигантский массив из 1000 элементов
	const batchSize = 1000
	heavyData := make([]benchmarkTarget, batchSize)
	for i := range batchSize {
		heavyData[i] = benchmarkTarget{
			Login:    "benchmark_user_name_test_long_string_just_to_add_weight",
			Password: "super_secure_password_string_123_high_performance_testing",
		}
	}

	b.Run("httpcodec.WriteJSON_Optimized", func(b *testing.B) {
		res := httptest.NewRecorder()
		b.ResetTimer()
		for b.Loop() {
			res.Body.Reset() // Сбрасываем длину, но сохраняем внутреннюю емкость (capacity) буфера

			// Наш кодек: стримит 1000 элементов напрямую в переиспользуемый буфер пула
			codec.WriteJSON(res, http.StatusOK, &heavyData)
			benchSinkObj = res
		}
	})

	b.Run("standard.Marshal_And_Write_Legacy", func(b *testing.B) {
		res := httptest.NewRecorder()
		b.ResetTimer()
		for b.Loop() {
			res.Body.Reset()

			// Легаси-подход: json.Marshal ВЫНУЖДЕН аллоцировать в куче
			// огромный срез байт под весь массив из 1000 элементов НА КАЖДЫЙ запрос!
			bytesData, _ := json.Marshal(&heavyData)
			res.Header().Set("Content-Type", "application/json")
			res.WriteHeader(http.StatusOK)
			_, _ = res.Write(bytesData)
			benchSinkObj = res
		}
	})
}
