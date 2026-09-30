package httpcodec

import (
	"bytes"
	"sync"
)

// Codec объединяет высокопроизводительные и потокобезопасные инструменты
// для симметричного кодирования и декодирования HTTP-данных.
//
// Структура инкапсулирует лимиты сетевого периметра и управляет двумя независимыми
// пулами переиспользования памяти (sync.Pool). Это позволяет минимизировать аллокации
// в куче (heap) и свести нагрузку на Garbage Collector (GC) к абсолютному минимуму (0 B/op).
type Codec struct {
	// maxBodySize определяет жесткий лимит размера тела входящего HTTP-запроса в байтах.
	maxBodySize int64

	// maxTrashRead определяет максимальный объем данных в байтах, вычитываемый в пустоту
	// (io.Discard) для очистки сокета при превышении лимита maxBodySize. Позволяет сохранить
	// и повторно использовать TCP-соединение (Keep-Alive / HTTP2).
	maxTrashRead int64

	// maxJSONBufferCap задает верхнюю границу емкости буфера ответа (capacity) в байтах.
	// Буферы, раздутые выше этого лимита, не возвращаются в пул, а утилизируются GC,
	// что предотвращает деградацию памяти (OOM) из-за редких тяжелых ответов.
	maxJSONBufferCap int

	// initJSONBufferCap определяет начальную преаллоцированную емкость буфера ответа в байтах.
	// Задание оптимального размера исключает динамическую реаллокацию памяти (runtime.growslice)
	// во время сериализации JSON.
	initJSONBufferCap int

	// bufferPool управляет пулом указателей на фиксированные массивы байт (*[]byte).
	// Используется для изолированного чтения входящих сетевых потоков в рамках лимита maxBodySize.
	bufferPool sync.Pool

	// jsonPool управляет пулом высокопроизводительных буферов (*bytes.Buffer).
	// Используется для потоковой сериализации исходящих REST API ответов без аллокаций в куче.
	jsonPool sync.Pool
}

// New создает и настраивает новый экземпляр Codec с поддержкой пулов памяти.
func New(maxBodySize int64, opts ...Option) *Codec {
	c := &Codec{
		maxBodySize:       maxBodySize,
		maxTrashRead:      defaultMaxTrashRead,
		maxJSONBufferCap:  defaultMaxJSONBufferCap,
		initJSONBufferCap: defaultInitJSONBufferCap,
	}

	for _, opt := range opts {
		opt(c)
	}

	c.bufferPool = sync.Pool{
		New: func() any {
			b := make([]byte, c.maxBodySize+1)
			return &b
		},
	}

	c.jsonPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, c.initJSONBufferCap))
		},
	}

	return c
}
