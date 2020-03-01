package sliceutil_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/abicky/ecsmec/internal/sliceutil"
)

func TestContains(t *testing.T) {
	if sliceutil.Contains([]string{"foo", "bar"}, "baz") {
		t.Errorf(`Contains([]string{"foo", "bar"}, "baz") = false; want false`)
	}
	if !sliceutil.Contains([]string{"foo", "bar"}, "foo") {
		t.Errorf(`Contains([]string{"foo", "bar"}, "foo") = true; want true`)
	}
}

func TestChunkSlice(t *testing.T) {
	n := 7
	strs := make([]*string, n)
	for i := 0; i < n; i++ {
		s := fmt.Sprint(i)
		strs[i] = &s
	}

	chunks := make([][]*string, 0)
	for chunk := range sliceutil.ChunkSlice(strs, 3) {
		chunks = append(chunks, chunk)
	}
	reflect.DeepEqual(chunks, [][]*string{
		{strs[0], strs[1], strs[2]},
		{strs[3], strs[4], strs[5]},
		{strs[6]},
	})
}
