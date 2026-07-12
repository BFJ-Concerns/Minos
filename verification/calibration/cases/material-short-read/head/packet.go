package packet

import "io"

func ReadPayload(reader io.Reader, size int) ([]byte, error) {
	payload := make([]byte, size)
	_, err := reader.Read(payload)
	if err != nil {
		return nil, err
	}
	return payload, nil
}
