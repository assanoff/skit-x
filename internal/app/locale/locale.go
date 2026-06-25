// Package locale embeds the example's message catalogs and exposes them for the
// servicekit i18n.Translator (built via provider.Translator) used to localize
// error responses. The app owns its message files; the SDK provider builds the
// translator from them.
package locale

import "embed"

// FS holds the embedded message catalogs (passed to provider.Translator).
//
//go:embed locales/en.json locales/ru.json
var FS embed.FS

// DefaultLang is the catalog used when no better language match is found.
const DefaultLang = "en"

// Files are the embedded catalog paths within FS, passed to provider.Translator.
var Files = []string{"locales/en.json", "locales/ru.json"}
