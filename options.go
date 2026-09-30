package httpcodec

// Дефолтные значения для конфигурационных параметров сетевого периметра.
const (
	defaultMaxTrashRead      int64 = 64 * 1024  // 64 KB на вычитку хвоста запроса в пустоту
	defaultMaxJSONBufferCap  int   = 128 * 1024 // 128 KB — максимальный размер буфера ответа в пуле
	defaultInitJSONBufferCap int   = 1024       // 1 KB — дефолтная начальная емкость буфера ответа
)

// Option определяет сигнатуру функции для гибкого конфигурирования Кодека.
type Option func(*Codec)

// WithMaxTrashRead настраивает лимит вычитки «мусора» в пустоту для спасения Keep-Alive.
// Позволяет сохранить прогретое соединение, если клиент слегка превысил maxBodySize.
func WithMaxTrashRead(bytes int64) Option {
	return func(c *Codec) {
		c.maxTrashRead = bytes
	}
}

// WithMaxJSONBufferCap задает верхнюю границу емкости буфера ответа.
// Если буфер при маршалинге раздуется сильнее этого лимита, он утилизируется GC (защита от OOM).
func WithMaxJSONBufferCap(bytes int) Option {
	return func(c *Codec) {
		c.maxJSONBufferCap = bytes
	}
}

// WithInitJSONBufferCap настраивает начальную емкость (pre-allocated capacity)
// для каждого нового bytes.Buffer, создаваемого внутри пула jsonPool.
func WithInitJSONBufferCap(bytes int) Option {
	return func(c *Codec) {
		c.initJSONBufferCap = bytes
	}
}
