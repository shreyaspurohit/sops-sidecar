package decrypt

import (
	"fmt"
	"log/slog"
)

// redacted replaces secret content wherever a Value or Identities is formatted.
const redacted = "[REDACTED]"

// Value is the decrypted content of a key. It never reveals its bytes when
// printed, formatted, or logged; only an explicit conversion to []byte does.
type Value []byte

// String returns a fixed placeholder.
func (v Value) String() string { return redacted }

// GoString returns a fixed placeholder for the %#v verb.
func (v Value) GoString() string { return redacted }

// Format writes a fixed placeholder for every verb.
func (v Value) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue returns a fixed placeholder for structured logging.
func (v Value) LogValue() slog.Value { return slog.StringValue(redacted) }

// Secret is a key together with its decrypted value.
type Secret struct {
	Key   string
	Value Value
}
