package httpcodec

import (
	"bytes"
	"net/http"

	json "github.com/goccy/go-json"
)

// WriteJSON выполняет высокопроизводительную потоковую сериализацию данных в JSON
// и отправку в сетевой сокет с защитой от OOM (Capacity Check). Полный исходный код
// доступен в документации.
func (c *Codec) WriteJSON(w http.ResponseWriter, statusCode int, data any) {
	buf := c.jsonPool.Get().(*bytes.Buffer)

	defer func() {
		if buf.Cap() <= c.maxJSONBufferCap {
			buf.Reset()
			c.jsonPool.Put(buf)
		}
	}()

	if err := json.NewEncoder(buf).Encode(data); err != nil {
		http.Error(w, "Failed to serialize response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = buf.WriteTo(w)
}
