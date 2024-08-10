package sliceutil

func Contains[T comparable](slice []T, v T) bool {
	for _, elem := range slice {
		if elem == v {
			return true
		}
	}
	return false
}

func ChunkSlice[T any](slice []T, size int) chan []T {
	ch := make(chan []T)
	go func() {
		for i := 0; i < len(slice); i += size {
			end := i + size
			if end > len(slice) {
				end = len(slice)
			}
			ch <- slice[i:end]
		}
		close(ch)
	}()

	return ch
}
