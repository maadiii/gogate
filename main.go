package main

import "fmt"

func main() {
	var a any = []string{"a", "b", "c"}

	c := a.([]string)
	fmt.Println(c)
}
