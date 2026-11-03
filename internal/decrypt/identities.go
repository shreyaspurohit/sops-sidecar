package decrypt

import (
	"bytes"
	"errors"
	"fmt"

	"filippo.io/age"
	sopsage "github.com/getsops/sops/v3/age"
)

// Identities holds the age identities (private keys) parsed from an age key
// file. It never reveals them when printed, formatted, or logged.
type Identities struct {
	ids sopsage.ParsedIdentities
}

// ParseIdentities parses an age key file; comments and blank lines are
// allowed. The error is a fixed message: age's parser messages are not passed
// on, so no key material can leak through it.
func ParseIdentities(keyFile []byte) (Identities, error) {
	ids, err := age.ParseIdentities(bytes.NewReader(keyFile))
	if err != nil || len(ids) == 0 {
		return Identities{}, errors.New("age key file holds no valid age identity (expected lines starting with AGE-SECRET-KEY-)")
	}
	return Identities{ids: ids}, nil
}

// Format writes a fixed placeholder for every verb.
func (i Identities) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }
