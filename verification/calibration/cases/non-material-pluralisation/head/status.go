package status

import "fmt"

func ItemCount(count int) string {
	return fmt.Sprintf("%d %s", count, "items")
}
