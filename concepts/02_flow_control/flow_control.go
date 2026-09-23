package main

import "fmt"

func main() {
	// FizzBuzz using for loop, if with init statement, and switch without expression
	for i := 1; i <= 100; i++ {
		// if with initialization statement
		if result := checkFizzBuzz(i); result != "" {
			fmt.Println(result)
			continue
		}

		// switch without expression (cleaner for multiple conditions)
		switch {
		case i%3 == 0 && i%5 == 0:
			fmt.Println("FizzBuzz")
		case i%3 == 0:
			fmt.Println("Fizz")
		case i%5 == 0:
			fmt.Println("Buzz")
		default:
			fmt.Println(i)
		}
	}
}

// helper function to demonstrate if with init statement
func checkFizzBuzz(n int) string {
	// if with initialization and multiple conditions
	if isFizz := n%3 == 0; isFizz {
		if isBuzz := n%5 == 0; isBuzz {
			return "FizzBuzz (from if)"
		}
		return "Fizz (from if)"
	}
	if isBuzz := n%5 == 0; isBuzz {
		return "Buzz (from if)"
	}
	return ""
}
