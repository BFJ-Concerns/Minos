package rangecheck

func Contains(value, minimum, maximum int) bool {
	return value >= minimum && value <= maximum
}

func Clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
