package vault

import (
	"crypto/rand"
	"math/big"
)

// Characters for generated passwords. Look-alikes (I, O, l, 0, 1) are left
// out for the times someone has to type one, and the symbols are ones that
// need no escaping in shells, URLs or LDAP.
var passwordClasses = []string{
	"ABCDEFGHJKLMNPQRSTUVWXYZ",
	"abcdefghijkmnopqrstuvwxyz",
	"23456789",
	"-_.!@#%+=",
}

// GeneratePassword returns a random password of length characters, with at
// least one character of each class, which satisfies Active Directory's
// complexity rules.
func GeneratePassword(length int) (string, error) {
	all := ""
	for _, c := range passwordClasses {
		all += c
	}
	out := make([]byte, 0, length)
	for _, c := range passwordClasses {
		ch, err := pick(c)
		if err != nil {
			return "", err
		}
		out = append(out, ch)
	}
	for len(out) < length {
		ch, err := pick(all)
		if err != nil {
			return "", err
		}
		out = append(out, ch)
	}
	// Shuffle so the guaranteed characters are not always first.
	for i := len(out) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		out[i], out[j.Int64()] = out[j.Int64()], out[i]
	}
	return string(out), nil
}

func pick(set string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
	if err != nil {
		return 0, err
	}
	return set[n.Int64()], nil
}
