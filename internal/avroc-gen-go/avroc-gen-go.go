package avrocgengo

import (
	"context"
	"fmt"

	"github.com/z5labs/avroc/internal/cli"
)

func Main(ctx context.Context, cli cli.Context) int {
	fmt.Println("hello world")
	return 0
}
