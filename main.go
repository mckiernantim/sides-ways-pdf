package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/gorilla/mux"
)

type PDFRequest struct {
	Name          string          `json:"name"`
	CallSheetPath string          `json:"callSheetPath"`
	ScriptData    json.RawMessage `json:"scriptData"`
	JwtToken      string          `json:"jwtToken"`
}

func main() {
	
	r := mux.NewRouter()
	r.HandleFunc("/", homeHandler).Methods("GET")
	r.HandleFunc("/generate-pdf", generatePDFHandler).Methods("POST")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Starting server on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "PDF Service is running")
}

func generatePDFHandler(w http.ResponseWriter, r *http.Request) {
	var req PDFRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	pdf, err := generatePDF(req)
	if err != nil {
		http.Error(w, "Failed to generate PDF: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.pdf", req.Name))
	w.Write(pdf)
}

func generatePDF(req PDFRequest) ([]byte, error) {
	// Create a new Chrome instance
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	// Create a timeout
	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Generate HTML content
	html, err := generateHTML(req)
	if err != nil {
		return nil, fmt.Errorf("failed to generate HTML: %v", err)
	}

	// Write HTML to a temporary file
	tmpfile, err := ioutil.TempFile("", "script-*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(html)); err != nil {
		return nil, fmt.Errorf("failed to write to temp file: %v", err)
	}
	if err := tmpfile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %v", err)
	}

	// Generate PDF
	var buf []byte
	if err := chromedp.Run(ctx,
		chromedp.Navigate("file://"+tmpfile.Name()),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			buf, _, err = page.PrintToPDF().WithPrintBackground(true).Do(ctx)
			return err
		}),
	); err != nil {
		return nil, fmt.Errorf("failed to generate PDF: %v", err)
	}

	return buf, nil
}

func generateHTML(req PDFRequest) (string, error) {
	// TODO: Implement your HTML generation logic here
	// This should use the req.ScriptData to generate the HTML content
	// You may want to use a template engine like html/template

	// For now, let's return a simple HTML structure
	return fmt.Sprintf(`
		<!DOCTYPE html>
		<html>
		<head>
			<title>%s</title>
			<style>
				body { font-family: Arial, sans-serif; }
			</style>
		</head>
		<body>
			<h1>%s</h1>
			<p>Script content goes here...</p>
		</body>
		</html>
	`, req.Name, req.Name), nil
}