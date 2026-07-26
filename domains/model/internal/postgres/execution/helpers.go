package execution

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
