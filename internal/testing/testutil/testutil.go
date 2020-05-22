package testutil

import (
	"github.com/golang/mock/gomock"
)

func InOrder(calls ...*gomock.Call) *gomock.Call {
	gomock.InOrder(calls...)
	return calls[len(calls)-1]
}

func MatchSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	aMap := make(map[string]int, len(a))
	for _, s := range a {
		aMap[s]++
	}
	for _, s := range b {
		if _, ok := aMap[s]; !ok {
			return false
		}
		aMap[s]--
		if aMap[s] == 0 {
			delete(aMap, s)
		}
	}

	if len(aMap) > 0 {
		return false
	}

	return true
}
