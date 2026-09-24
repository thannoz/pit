package main

import "fmt"

// price shows an amount in cents the way the shop prints it.
func price(cents int) string {
	return fmt.Sprintf("%d.%02d EUR", cents/100, cents%100)
}
