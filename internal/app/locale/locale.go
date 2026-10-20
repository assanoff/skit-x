// Package locale embeds the example's message catalogs and exposes them for the
// skit i18n.Translator (built via provider/translator) used to localize
// error responses. The app owns its message files; the SDK factory builds the
// translator from them.
package locale

import "embed"

// FS holds the embedded message catalogs (passed to translator.Resource).
//
//go:embed locales/en.json locales/ru.json
var FS embed.FS

// DefaultLang is the catalog used when no better language match is found.
const DefaultLang = "en"

// Files are the catalog paths within FS, passed to translator.Resource.
var Files = []string{"locales/en.json", "locales/ru.json"}
