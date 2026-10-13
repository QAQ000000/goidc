package supplier

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"strings"
)

// Only provider-generated, non-editable credentials are supported. Customer
// supplied credentials need a separate purchase-field contract.
type credentialRule struct {
	Prefix string
	Length int
	Groups []string
}

func parseCredentialRule(raw json.RawMessage, host bool) (*credentialRule, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	show, valid := productRule(raw, "show")
	if !valid {
		return nil, false
	}
	if show == "" || show == "0" {
		return nil, true
	}
	modify, valid := productRule(raw, "modify")
	if show != "1" || !valid || modify != "0" {
		return nil, false
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, false
	}
	var rule map[string]json.RawMessage
	if json.Unmarshal(fields["rule"], &rule) != nil {
		return nil, false
	}
	length := number(rule["len_num"])
	if length < 8 || length > 64 {
		return nil, false
	}
	out := &credentialRule{Length: int(length)}
	if host {
		if len(fields["prefix"]) > 0 && json.Unmarshal(fields["prefix"], &out.Prefix) != nil {
			return nil, false
		}
		if len(out.Prefix)+out.Length > 63 || strings.Trim(out.Prefix, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
			return nil, false
		}
	}
	for _, group := range []struct{ key, alphabet string }{{"upper", "ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, {"lower", "abcdefghijklmnopqrstuvwxyz"}, {"num", "0123456789"}, {"special", "!@#$%"}} {
		value := "0"
		if rawValue := rule[group.key]; len(rawValue) > 0 && json.Unmarshal(rawValue, &value) != nil {
			value = string(rawValue)
		}
		if value != "0" && value != "1" {
			return nil, false
		}
		if value == "1" {
			if host && group.key == "special" {
				return nil, false
			}
			out.Groups = append(out.Groups, group.alphabet)
		}
	}
	return out, len(out.Groups) > 0
}

func (r *credentialRule) generate() (string, error) {
	chars := make([]byte, r.Length)
	alphabet := strings.Join(r.Groups, "")
	for i := range chars {
		group := alphabet
		if i < len(r.Groups) {
			group = r.Groups[i]
		}
		choice, err := rand.Int(rand.Reader, big.NewInt(int64(len(group))))
		if err != nil {
			return "", err
		}
		chars[i] = group[choice.Int64()]
	}
	// Shuffle so required character classes do not have fixed positions.
	for i := len(chars) - 1; i > 0; i-- {
		choice, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		chars[i], chars[choice.Int64()] = chars[choice.Int64()], chars[i]
	}
	return r.Prefix + string(chars), nil
}
