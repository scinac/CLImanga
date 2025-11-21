package manga

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	logger "github.com/scinac/CLImanga/internal/log"
)

const (
	baseURL     string = "https://api.mangadex.org"
	downloadURL string = "https://uploads.mangadex.org"
)

func getFullBuiltURL(apiURL *string, params *url.Values) string {
	return *apiURL + "?" + params.Encode()
}

func makeGETApiRequest(fullAPIURL *string) (map[string]any, error) {
	response, err := http.Get(*fullAPIURL)
	if err != nil {
		return nil, fmt.Errorf("error making GET request %v", err)
	}

	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading GET response body %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("error parsing JSON: %v", err)
	}

	return result, nil
}

func FetchMangasByNameSearch(mangaName *string) ([]MangaSelect, error) {
	apiURl := baseURL + "/manga"

	params := url.Values{}
	params.Add("title", *mangaName)
	params.Add("order[relevance]", "desc")

	fullAPIURL := getFullBuiltURL(&apiURl, &params)

	data, err := makeGETApiRequest(&fullAPIURL)
	if err != nil {
		return nil, err
	}

	mangasFound := []MangaSelect{}

	dataArr, ok := data["data"].([]any) // json path: data(id)(attributes(title(en,..)))

	if !ok {
		return nil, fmt.Errorf("couldnt get data")
	}

	for _, item := range dataArr {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		mangaID := itemMap["id"].(string) // hashCodeID (like asdad-asdad-asda)

		attributes, ok := itemMap["attributes"].(map[string]any)
		if !ok {
			continue
		}

		if title, ok := attributes["title"].(map[string]any); ok {
			if en, ok := title["en"].(string); ok {
				mangasFound = append(mangasFound, MangaSelect{
					ID:   mangaID,
					Name: en,
				})
			} else {
				if jp, ok := title["jp"].(string); ok {
					mangasFound = append(mangasFound, MangaSelect{
						ID:   mangaID,
						Name: jp,
					})
				}
			}
		}
	}
	return mangasFound, nil
}

func DownloadEntireManga(mangaID *string, mangaName *string) error {
	chapterList, err := GetAllChapterListOfManga(mangaID)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup

	maxConcurrent := 5                        // max threads running
	sem := make(chan struct{}, maxConcurrent) // sephamore

	for _, chapter := range chapterList {
		wg.Add(1)

		sem <- struct{}{}

		go func(chapter ChapterSelect) {
			defer wg.Done()
			defer func() { <-sem }() // release sephamore slot
			err := DownloadMangaChapter(&chapter.ID, mangaName, &chapter.ChapterNumber, "downloads", nil)
			if err != nil {
				log.Println("Chapter-" + chapter.ChapterNumber + " failed to donwload.")
				logger.Error.Printf("Chapter-%s download failed: %v", chapter.ChapterNumber, err)
			} else {
				log.Println("Chapter-" + chapter.ChapterNumber + " downloaded.")
			}
		}(chapter)

	}

	return nil
}

func GetAllChapterListOfManga(mangaID *string) ([]ChapterSelect, error) { // https://api.mangadex.org/docs/04-chapter/search/
	APIURL := baseURL + "/manga/" + *mangaID + "/feed"
	params := url.Values{}
	params.Add("offset", "0")
	params.Add("translatedLanguage[]", "en")
	params.Add("order[chapter]", "asc")
	fullAPIURL := getFullBuiltURL(&APIURL, &params)

	data, err := makeGETApiRequest(&fullAPIURL)
	if err != nil {
		return nil, err
	}

	dataArr, ok := data["data"].([]any)
	if !ok {
		return nil, fmt.Errorf("couldnt get data")
	}

	chapterList := []ChapterSelect{}

	for index, item := range dataArr { // path: data(id)(attributes)
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		attributes, ok := itemMap["attributes"].(map[string]any)

		if !ok {
			return nil, fmt.Errorf("couldnt get chapter list")
		}

		chapterID, _ := itemMap["id"].(string)

		// Just handle empty title
		title := "Untitled"
		if t, ok := attributes["title"].(string); ok && t != "" {
			title = t
		}

		pages := attributes["pages"].(float64)
		chapterNumber := attributes["chapter"].(string)

		chapterList = append(chapterList, ChapterSelect{
			Index:         index,
			ID:            chapterID,
			ChapterNumber: chapterNumber,
			Title:         title,
			Pages:         pages,
		})

	}
	return chapterList, nil
}

func DownloadMangaChapter(chapterID *string, mangaName *string, chapterNumber *string, filePathDirectory string, out chan<- string) error {
	chapterImageIDs, hash, err := retrieveMangaChapterImagesIDs(chapterID)
	if err != nil {
		return err
	}

	dir := filepath.Join("resources", filePathDirectory, ""+*mangaName, "chapter-"+*chapterNumber)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	for i, filename := range chapterImageIDs {
		imageURL := fmt.Sprintf("%s/data-saver/%s/%s", downloadURL, hash, filename)
		savePath := filepath.Join(dir, fmt.Sprintf("page_%03d%s", i+1, filepath.Ext(filename)))

		logger.Info.Printf("Downloading image URL: %s", imageURL)
		if err := downloadFile(imageURL, savePath); err != nil {
			log.Printf("Failed to download %s: %v", imageURL, err)
			logger.Error.Printf("Failed to download %s: %v", imageURL, err)
		} else {
			logger.Info.Printf("Downloaded and saved to %s", savePath)
		}

		// log.Printf("Saved page %d: %s", i+1, savePath)

		out <- savePath
	}

	close(out)
	return nil
}

func retrieveMangaChapterImagesIDs(chapterID *string) ([]string, string, error) { // https://api.mangadex.org/docs/04-chapter/retrieving-chapter/
	baseURL := "https://api.mangadex.org/at-home/server/" + *chapterID
	// params := url.Values{}

	data, err := makeGETApiRequest(&baseURL)
	if err != nil {
		return nil, "", fmt.Errorf("error when making API request %v", err)
	}

	// log.Print(data)
	dataArr, ok := data["chapter"].(map[string]any) // json path: data(chapter(data[] (.png) or dataSaver[] for lower quality(.jpg)))
	// log.Print(dataArr)

	if !ok {
		return nil, "", fmt.Errorf("couldnt get data")
	}

	var chapterImageIDs []string
	var hash string = dataArr["hash"].(string)

	if dataServerImages, ok := dataArr["dataSaver"].([]any); ok {
		for _, chapterImageHashID := range dataServerImages {
			chapterImageIDs = append(chapterImageIDs, chapterImageHashID.(string))
		}
	}
	return chapterImageIDs, hash, nil
}

func downloadFile(url, path string) error {
	const maxRetries = 5
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err = func() error {
			resp, err := http.Get(url)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("bad status: %s", resp.Status)
			}

			outFile, err := os.Create(path)
			if err != nil {
				return err
			}
			defer outFile.Close()

			_, err = io.Copy(outFile, resp.Body)
			return err
		}()
		if err == nil {
			return nil // success
		}
		logger.Error.Printf("Attempt %d: Failed to download %s: %v", attempt+1, url, err)
		time.Sleep(time.Second * 4) // If failed, wait and retry
	}

	// If all attempts fail, return the last error
	return fmt.Errorf("failed to download file from %s after %d attempts: %v", url, maxRetries, err)
}
