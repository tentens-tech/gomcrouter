package time

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNanoTime_Monotonic(t *testing.T) {
	prev := NanoTime()
	assert.Positive(t, prev)
	for i := 0; i < 1000; i++ {
		now := NanoTime()
		assert.GreaterOrEqual(t, now, prev)
		prev = now
	}
}
