package domain

import (
	"strings"
	"unicode/utf8"
)

const MaxAcceptanceCriteriaBytes = 16 * 1024

func ValidAcceptanceCriteria(value string) bool {
	return utf8.ValidString(value) && len(value) <= MaxAcceptanceCriteriaBytes && !strings.ContainsRune(value, '\x00')
}
