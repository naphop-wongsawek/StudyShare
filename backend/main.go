
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	response := map[string]string{
		"message": "StudyShare API is running!",
		"status":  "success",
	}

	json.NewEncoder(w).Encode(response)
}

func main() {
	http.HandleFunc("/api/health", healthHandler)

	fmt.Println("StudyShare Backend running on port 8080")

	log.Fatal(http.ListenAndServe(":8080", nil))
}
