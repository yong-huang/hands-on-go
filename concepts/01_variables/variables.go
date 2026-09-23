package main

import "fmt"

func main() {
	// var declarations with explicit types
	var age int = 30
	var name string = "Alice"
	var isActive bool = true
	var pi float64 = 3.14159

	// var declarations with type inference
	var city = "New York"
	var score = 95.5
	var flag = false

	// short variable declarations (inside functions only)
	country := "Canada"
	height := 5.9
	population := 38000000
	valid := true

	// multiple var declaration block
	var (
		product string  = "Laptop"
		price   float64 = 999.99
		inStock bool    = true
	)

	// constants
	const appName string = "GoTypeDemo"
	const version = 1.0
	const debug = false

	// print types using fmt.Printf("%T")
	fmt.Printf("age: %T\n", age)
	fmt.Printf("name: %T\n", name)
	fmt.Printf("isActive: %T\n", isActive)
	fmt.Printf("pi: %T\n", pi)
	fmt.Printf("city: %T\n", city)
	fmt.Printf("score: %T\n", score)
	fmt.Printf("flag: %T\n", flag)
	fmt.Printf("country: %T\n", country)
	fmt.Printf("height: %T\n", height)
	fmt.Printf("population: %T\n", population)
	fmt.Printf("valid: %T\n", valid)
	fmt.Printf("product: %T\n", product)
	fmt.Printf("price: %T\n", price)
	fmt.Printf("inStock: %T\n", inStock)
	fmt.Printf("appName: %T\n", appName)
	fmt.Printf("version: %T\n", version)
	fmt.Printf("debug: %T\n", debug)
}
