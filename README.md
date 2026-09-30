# httpcodec 🚀

Высокопроизводительный, потокобезопасный Open-Source пакет для Go 1.24+ (с поддержкой Go 1.26.1), обеспечивающий симметричное кодирование/декодирование HTTP с нулевыми аллокациями (Zero-Allocation I/O) и защитой периметра для финтех и highload систем (версия **v0.0.2** использует модульную архитектуру и Functional Options).

## ✨ Ключевые особенности v0.0.2

*   **Модульная архитектура:** Разделение на `options.go`, `codec.go`, `reader.go`, `writer.go`, `middleware.go`.
*   **Functional Options:** Тонкий тюнинг буферов и лимитов.
*   **Zero-Allocation I/O:** Оптимизация через `sync.Pool`.
*   **OOM Protection:** Автоматическая утилизация раздутых буферов.
*   **Fail-Safe Keep-Alive:** Очистка до 64 KB в `io.Discard` для сохранения соединений.
*   **Data Bleed Protection:** Зануление через `clear()` для изоляции данных.
*   **Drop-in JIT JSON:** Использование `goccy/go-json`.

## 🏗 Технологический стек

* **Среда выполнения:** Go 1.24 – 1.26.1+
* **Базовый парсер:** `github.com/goccy/go-json`
* **Совместимость:** Полная совместимость со стандартным пакетом `net/http` и любыми роутерами (`chi`, `gin`, `gorilla/mux`).

## ⚡ Результаты бенчмарков (v0.0.2)

Тестирование производительности ядра пакета на процессоре **AMD Ryzen 5 5600X (12 потоков)** с флагами `-benchtime=2s -count=10` продемонстрировало следующие чистые показатели эффективности утилизации памяти (метрики очищены от оверхеда пакета `httptest`):

| Метод / Компонент | Скорость выполнения | Потребление памяти (`B/op`) | Аллокации (`allocs/op`) |
| :--- | :--- | :--- | :--- |
| **ReadBytes_Optimized** | `125.0 – 129.8 ns/op` | **`0 B/op`** | **`0 allocs/op`** |
| **ReadJSON_Optimized** | `249.4 – 263.3 ns/op` | **`88 B/op`** | **`1 allocs/op`** |
| **WriteJSON_Optimized (Light)** | `145.3 – 151.2 ns/op` | **`16 B/op`** | **`1 allocs/op`¹** |
| **WriteHeavyJSON_Optimized (1K Batch)** | `33.1 – 35.4 µs/op` | **`17 – 34 B/op`** | **`1 allocs/op`¹** |

> **¹ Важное примечание по аллокациям на запись:** Единичная аллокация (`1 allocs/op`) и сопутствующие `16–34 B/op` возникают исключительно из-за вызова стандартного метода `w.Header().Set("Content-Type", "application/json")` пакета `net/http`, который неявно выделяет кучевую память под срез строк заголовка (`[]string`). **Сама сериализация JSON и утилизация пулов внутри кодека происходят с абсолютным нулем аллокаций.**

### 📊 Сравнительный Highload-анализ с Legacy подходом:

* **Истинный Zero-Allocation на чтении**: Метод `ReadBytes` при чистом проходе рантайма гарантирует честные **`0 B/op`** и **`0 allocs/op`**, полностью исключая нагрузку на Garbage Collector .
* **Экстремальное сжатие кучи (Heavy JSON)**: На тяжелых батчах из 1000 элементов кодек расходует всего **`~25 байт`** памяти и отрабатывает за **`~33 µs`** . Стандартный легаси-подход (`json.Marshal`) на том же процессоре требует **`141.5 KB`** памяти на операцию и выполняется на 44% медленнее (**`48.5 µs`**).
* **OOM и Data Bleed защита**: Накладные расходы включают в себя сквозную верификацию `buf.Cap()` для предотвращения деградации памяти из-за раздувания пулов и зануление буферов через `clear()`.

## 📦 Установка

```bash
go get github.com/ioncode/httpcodec
```

## 🚀 Быстрый старт

### 1. Инициализация и защита периметра (main.go)

Настройте кодек с использованием функциональных опций для ограничения входящего тела, очистки сокета Keep-Alive и управления пулом буферов. Подключите встроенное Middleware в роутер:

```go
package main

import (
	"net/http"
	"time"

	"github.com/ioncode/httpcodec"
	"github.com/go-chi/chi/v5"
)

func main() {
	r := chi.NewRouter()

	codec := httpcodec.New(
		8192,
		httpcodec.WithMaxTrashRead(64*1024),
		httpcodec.WithInitJSONBufferCap(4*1024),
		httpcodec.WithMaxJSONBufferCap(256*1024),
	)

	r.Use(codec.Middleware())
	r.Post("/api/v1/batch", APIPostBatchHandler(codec))

	server := &http.Server{
		Addr:         ":8080",
		Handler:      r,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	_ = server.ListenAndServe()
}
```

### 2. Использование в хендлерах (handler.go)

Чтение и отправка данных выполняются через методы кодека, скрывающие управление памятью:

```go
package main

import (
	"net/http"
	"github.com/ioncode/httpcodec"
)

type RequestItem struct {
	CorrelationID string `json:"correlation_id"`
	Payload       string `json:"payload"`
}

type ResponseDTO struct {
	Status string   `json:"status"`
	IDs    []string `json:"processed_ids"`
}

func APIPostBatchHandler(codec *httpcodec.Codec) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var items []RequestItem

		if !codec.ReadJSON(w, r, &items) {
			return 
		}

		processedIDs := make([]string, len(items))
		for i, item := range items {
			processedIDs[i] = item.CorrelationID
		}

		response := ResponseDTO{
			Status: "success",
			IDs:    processedIDs,
		}

		codec.WriteJSON(w, http.StatusCreated, &response)
	}
}
```

## 📜 Лицензия

MIT License. См. файл `LICENSE` для подробностей.
