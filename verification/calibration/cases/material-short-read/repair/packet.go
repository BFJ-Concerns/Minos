package packet

import "io"

func ReadPayload(reader io.Reader, size int) ([]byte, error) {
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
