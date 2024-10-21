package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
    "regexp"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"io/ioutil"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/gorilla/mux"
	"github.com/jung-kurt/gofpdf"

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
    Visible             bool    `json:"visible"`
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
    WatermarkText       string  `json:"watermarkText"`
    DraftColorText      string  `json:"draftColorText"`
    PageNumberText      string  `json:"pageNumberText"`
    Hidden              string  `json:"hidden"`
    IsRevision          string  `json:"isRevision"`
    BarY                float64 `json:"barY"`
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

func generatePDFHandler(w http.ResponseWriter, r *http.Request) {
	var req PDFRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	pdf, err := generatePDF(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to generate PDF: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=generated.pdf")
	w.Write(pdf)
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "PDF Service is running")
}

func generateMainPDF(req PDFRequest) ([]byte, error) {
    // Generate HTML from the script data
    html, err := generateHTML(req)
    if err != nil {
        return nil, fmt.Errorf("failed to generate HTML: %v", err)
    }

    // Convert HTML to PDF using Chrome headless
    ctx, cancel := chromedp.NewContext(context.Background())
    defer cancel()

    var buf []byte
    if err := chromedp.Run(ctx,
        chromedp.Navigate("about:blank"),
        chromedp.ActionFunc(func(ctx context.Context) error {
            frameTree, err := page.GetFrameTree().Do(ctx)
            if err != nil {
                return err
            }
            return page.SetDocumentContent(frameTree.Frame.ID, html).Do(ctx)
        }),
        chromedp.WaitReady("body"),
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

func generatePDF(req PDFRequest) ([]byte, error) {
    mainPDF, err := generateMainPDF(req)
    if err != nil {
        return nil, fmt.Errorf("failed to generate main PDF: %v", err)
    }

    if req.CallSheetPath != "" {
        callsheetPDF, err := processCallsheet(req.CallSheetPath)
        if err != nil {
            return nil, fmt.Errorf("failed to process callsheet: %v", err)
        }
        return mergePDFs(callsheetPDF, mainPDF)
    }

    return mainPDF, nil
}

func mergePDFs(pdf1, pdf2 []byte) ([]byte, error) {
    return basicPDFMerge(pdf1, pdf2)
}

func basicPDFMerge(pdf1, pdf2 []byte) ([]byte, error) {
    // Regular expressions to find PDF components
    reStartxref := regexp.MustCompile(`(?m)^startxref\s*\n\d+\s*\n%%EOF`)
    reObj := regexp.MustCompile(`\n(\d+) 0 obj`)

    // Find and remove the last startxref section from the first PDF
    pdf1 = reStartxref.ReplaceAll(pdf1, []byte{})

    // Find the highest object number in the first PDF
    matches := reObj.FindAllSubmatch(pdf1, -1)
    highestObj := 0
    for _, match := range matches {
        obj, err := strconv.Atoi(string(match[1]))
        if err != nil {
            return nil, fmt.Errorf("failed to parse object number: %v", err)
        }
        if obj > highestObj {
            highestObj = obj
        }
    }

    // Adjust object numbers in the second PDF
    pdf2Str := string(pdf2)
    objMap := make(map[string]string)
    stringMatches := reObj.FindAllStringSubmatch(pdf2Str, -1)
    for _, match := range stringMatches {
        oldObj := match[1]
        newObj := strconv.Itoa(highestObj + 1)
        objMap[oldObj] = newObj
        highestObj++
    }

    for oldObj, newObj := range objMap {
        pdf2Str = regexp.MustCompile(`\b`+regexp.QuoteMeta(oldObj)+` 0 obj\b`).ReplaceAllString(pdf2Str, newObj+" 0 obj")
        pdf2Str = regexp.MustCompile(`\b`+regexp.QuoteMeta(oldObj)+` 0 R\b`).ReplaceAllString(pdf2Str, newObj+" 0 R")
    }

    // Combine PDFs
    mergedPDF := append(pdf1, []byte(pdf2Str)...)

    // Update PDF header
    mergedPDF = append([]byte("%PDF-1.7\n"), mergedPDF...)

    // Add new startxref and EOF
    xrefIndex := len(mergedPDF)
    mergedPDF = append(mergedPDF, []byte(fmt.Sprintf("\nstartxref\n%d\n%%EOF", xrefIndex))...)

    return mergedPDF, nil
}

func processCallsheet(callsheetPath string) ([]byte, error) {
    fileExt := filepath.Ext(callsheetPath)
    switch fileExt {
    case ".pdf":
        return ioutil.ReadFile(callsheetPath)
    case ".png", ".jpg", ".jpeg":
        return convertImageToPDF(callsheetPath)
    default:
        return nil, fmt.Errorf("unsupported file type: %s", fileExt)
    }
}


func convertImageToPDF(imagePath string) ([]byte, error) {
    pdf := gofpdf.New("P", "mm", "A4", "")
    pdf.AddPage()

    imageInfo := pdf.RegisterImage(imagePath, "")
    if imageInfo == nil {
        return nil, fmt.Errorf("failed to register image")
    }

    imgWidth, imgHeight := imageInfo.Extent()
    pageWidth, pageHeight := pdf.GetPageSize()
    
    ratio := math.Min(pageWidth/imgWidth, pageHeight/imgHeight)
    width := imgWidth * ratio
    height := imgHeight * ratio

    pdf.Image(imagePath, (pageWidth-width)/2, (pageHeight-height)/2, width, height, false, "", 0, "")

    var buf bytes.Buffer
    err := pdf.Output(&buf)
    if err != nil {
        return nil, fmt.Errorf("failed to generate PDF from image: %v", err)
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