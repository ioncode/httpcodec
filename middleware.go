// Package httpcodec предоставляет высокопроизводительные инструменты для кодирования
// и декодирования HTTP-данных с нулевыми аллокациями и защитой периметра.
package httpcodec

import (
	"net/http"
)

// Middleware возвращает стандартный перехватчик (http.Handler middleware), который
// аппаратно ограничивает размер тела входящего запроса на уровне TCP-сокета.
//
// Метод полностью оптимизирован под рантайм Go 1.26.1 и использует лимит maxBodySize,
// заданный при инициализации кодека через конструктор New(). Для модифицирующих методов
// (POST, PUT, PATCH) поток r.Body оборачивается в http.MaxBytesReader.
//
// Это гарантирует автоматическую синхронизацию лимитов сетевого периметра и пулов памяти,
// предотвращая атаки класса Slowloris DoS и переполнение буферов.
func (c *Codec) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
				// Ограничиваем сетевой поток жестким лимитом кодека
				r.Body = http.MaxBytesReader(w, r.Body, c.maxBodySize)
			}
			next.ServeHTTP(w, r)
		})
	}
}
