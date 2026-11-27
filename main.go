// The root command preserves go run . and installs by module path.
package main

import "github.com/hieuvoq/chain-event-indexer/internal/app"

func main() {
	app.Main()
}
