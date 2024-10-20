package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/jpeg"
	"image/png"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/gorilla/mux"
	"github.com/jung-kurt/gofpdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
)

type PDFRequest struct {
	Name          string          `json:"name"`
	CallSheetPath string          `json:"callSheetPath"`
	ScriptData    json.RawMessage `json:"scriptData"`
	JwtToken      string          `json:"jwtToken"`
}

type LineData struct {
	Category            string  `json:"category"`
	SubCategory         string  `json:"subCategory"`
	CalculatedYpos      string  `json:"calculatedYpos"`
	CalculatedEnd       string  `json:"calculatedEnd"`
	YPos                float64 `json:"yPos"`
	XPos                float64 `json:"xPos"`
	EndY                float64 `json:"endY"`
	Visible             string  `json:"visible"`
	Bar                 string  `json:"bar"`
	SceneIndex          int     `json:"sceneIndex"`
	TrueScene           string  `json:"trueScene"`
	End                 string  `json:"end"`
	Cont                string  `json:"cont"`
	HideEnd             string  `json:"hideEnd"`
	HideCont            string  `json:"hideCont"`
	SceneNumberText     string  `json:"sceneNumberText"`
	HideSceneNumberText string  `json:"hideSceneNumberText"`
	Text                string  `json:"text"`
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
	var callsheetPDF []byte
	var err error

	if req.CallSheetPath != "" {
		callsheetPDF, err = processCallsheet(req.CallSheetPath)
		if err != nil {
			return nil, fmt.Errorf("failed to process callsheet: %v", err)
		}
	}

	mainPDF, err := generateMainPDF(req)
	if err != nil {
		return nil, fmt.Errorf("failed to generate main PDF: %v", err)
	}

	if callsheetPDF != nil {
		return mergePDFs(callsheetPDF, mainPDF)
	}

	return mainPDF, nil
}

func processCallsheet(callsheetPath string) ([]byte, error) {
	fileExt := strings.ToLower(filepath.Ext(callsheetPath))

	switch fileExt {
	case ".pdf":
		return ioutil.ReadFile(callsheetPath)
	case ".jpg", ".jpeg", ".png":
		return convertImageToPDF(callsheetPath)
	default:
		return nil, fmt.Errorf("unsupported file type: %s", fileExt)
	}
}

func convertImageToPDF(imagePath string) ([]byte, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open image: %v", err)
	}
	defer file.Close()

	img, format, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %v", err)
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()

	bounds := img.Bounds()
	imgWidth := float64(bounds.Max.X)
	imgHeight := float64(bounds.Max.Y)

	pageWidth, pageHeight := pdf.GetPageSize()
	scale := math.Min(pageWidth/imgWidth, pageHeight/imgHeight)

	tempImgPath := filepath.Join(os.TempDir(), "temp_callsheet."+format)
	tempImgFile, err := os.Create(tempImgPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp image file: %v", err)
	}
	defer os.Remove(tempImgPath)

	switch format {
	case "jpeg":
		jpeg.Encode(tempImgFile, img, nil)
	case "png":
		png.Encode(tempImgFile, img)
	default:
		return nil, fmt.Errorf("unsupported image format: %s", format)
	}

	pdf.Image(tempImgPath, 0, 0, imgWidth*scale, imgHeight*scale, false, "", 0, "")

	var buf bytes.Buffer
	err = pdf.Output(&buf)
	if err != nil {
		return nil, fmt.Errorf("failed to generate PDF from image: %v", err)
	}

	return buf.Bytes(), nil
}

func generateMainPDF(req PDFRequest) ([]byte, error) {
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	html, err := generateHTML(req)
	if err != nil {
		return nil, fmt.Errorf("failed to generate HTML: %v", err)
	}

	var buf []byte
	if err := chromedp.Run(ctx,
		chromedp.Navigate("data:text/html,"+html),
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

func mergePDFs(pdf1, pdf2 []byte) ([]byte, error) {
	tempFile1, err := ioutil.TempFile("", "pdf1-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for PDF1: %v", err)
	}
	defer os.Remove(tempFile1.Name())
	if _, err := tempFile1.Write(pdf1); err != nil {
		return nil, fmt.Errorf("failed to write PDF1 to temp file: %v", err)
	}
	tempFile1.Close()

	tempFile2, err := ioutil.TempFile("", "pdf2-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for PDF2: %v", err)
	}
	defer os.Remove(tempFile2.Name())
	if _, err := tempFile2.Write(pdf2); err != nil {
		return nil, fmt.Errorf("failed to write PDF2 to temp file: %v", err)
	}
	tempFile2.Close()

	var buf bytes.Buffer
	if err := api.MergeFile([]string{tempFile1.Name(), tempFile2.Name()}, &buf, nil); err != nil {
		return nil, fmt.Errorf("failed to merge PDFs: %v", err)
	}

	return buf.Bytes(), nil
}

func generateHTML(req PDFRequest) (string, error) {
    templatePath := filepath.Join("templates", "double.html")
    templateContent, err := ioutil.ReadFile(templatePath)
    if err != nil {
        return "", fmt.Errorf("failed to read template file: %v", err)
    }

    funcMap := template.FuncMap{
        "formatX": func(x float64) string {
            return fmt.Sprintf("%.2fpx", x)
        },
        "formatY": func(y float64) string {
            return fmt.Sprintf("%.2fpx", y)
        },
        "formatEndY": func(y float64) string {
            return fmt.Sprintf("%.2fpx", y)
        },
        "dualSceneNum": func(line LineData) string {
            return fmt.Sprintf("%.2fpx", line.YPos)
        },
        "startSingle": func(y float64) string {
            return fmt.Sprintf("%.2fpx", y)
        },
    }

    tmpl, err := template.New("pdf").Funcs(funcMap).Parse(string(templateContent))
    if err != nil {
        return "", fmt.Errorf("failed to parse template: %v", err)
    }

    var sceneData [][]LineData
    err = json.Unmarshal(req.ScriptData, &sceneData)
    if err != nil {
        return "", fmt.Errorf("failed to unmarshal script data: %v", err)
    }

    doubleCheckLinePositionsAndHiddenValuesBeforePDFGeneration(sceneData)

    data := map[string]interface{}{
        "Name":       req.Name,
        "ScriptData": sceneData,
    }

    var buf bytes.Buffer
    if err := tmpl.Execute(&buf, data); err != nil {
        return "", fmt.Errorf("failed to execute template: %v", err)
    }

    return buf.String(), nil
}

func doubleCheckLinePositionsAndHiddenValuesBeforePDFGeneration(sceneData [][]LineData) {
	starts := make(map[int]bool)
	contExemptValues := []string{"CONTINUE", "CONTINUE-TOP"}

	for _, page := range sceneData {
		for i := range page {
			line := &page[i]
			if line.Category == "injected-break" {
				line.Bar = ""
				line.Visible = "false"
			}
			yPos, _ := strconv.ParseFloat(line.CalculatedYpos, 64)
			line.YPos = yPos / 1.3
			if line.CalculatedEnd != "" {
				endY, _ := strconv.ParseFloat(line.CalculatedEnd, 64)
				line.EndY = endY / 1.3
			}

			if line.SubCategory == "CON'T" {
				line.Visible = "true"
			}

			if line.Bar == "bar" && !starts[line.SceneIndex] && line.SceneIndex > 0 {
				starts[line.SceneIndex] = true
			} else {
				line.Bar = "hide-bar"
			}

			if line.Category == "scene-header" && line.Visible == "true" {
				line.TrueScene = "true-scene"
				line.Bar = "start-bar"
				line.HideEnd = "hideEnd"
				line.HideCont = "hideCont"
				line.EndY = line.YPos
			}

			if line.End == "END" && starts[line.SceneIndex] {
				line.EndY = line.YPos - 5
				line.HideCont = "hideCont"
				line.Bar = "hideBar"
			}

			if line.Cont != "" && line.Cont != "hideCont" && starts[line.SceneIndex] && line.Bar != "start-bar" {
				line.HideEnd = "hideEnd"
				line.Bar = "hideBar"
			} else if line.TrueScene == "" && !contains(contExemptValues, line.Cont) {
				line.Bar = "hideBar"
			}

			if line.SceneNumberText != "" && line.Category != "scene-header" {
				line.HideSceneNumberText = "hidden"
			}
		}
	}
}

func contains(slice []string, item string) bool {
	for _, a := range slice {
		if a == item {
			return true
		}
	}
	return false
}