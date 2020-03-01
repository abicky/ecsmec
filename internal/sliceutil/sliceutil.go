package sliceutil

func Contains(slice []string, str string) bool {
	for _, elem := range slice {
		if elem == str {
			return true
		}
	}
	return false
}

func ChunkSlice(slice []*string, size int) chan []*string {
	ch := make(chan []*string)
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
