package webserver

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	fp "path/filepath"
	"strconv"
	"strings"
	"sync"

	"shiori/internal/core"
	"shiori/internal/database"
	"shiori/internal/model"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/crypto/bcrypt"
)

// apiGetBookmarks is handler for GET /api/bookmarks
func (h *handler) apiGetBookmarks(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Get URL queries
	keyword := r.URL.Query().Get("keyword")
	strPage := r.URL.Query().Get("page")
	strTags := r.URL.Query().Get("tags")
	strExcludedTags := r.URL.Query().Get("exclude")

	tags := strings.Split(strTags, ",")
	if len(tags) == 1 && tags[0] == "" {
		tags = []string{}
	}

	excludedTags := strings.Split(strExcludedTags, ",")
	if len(excludedTags) == 1 && excludedTags[0] == "" {
		excludedTags = []string{}
	}

	page, _ := strconv.Atoi(strPage)
	if page < 1 {
		page = 1
	}

	// Prepare filter for database
	searchOptions := database.GetBookmarksOptions{
		Tags:         tags,
		ExcludedTags: excludedTags,
		Keyword:      keyword,
		Limit:        30,
		Offset:       (page - 1) * 30,
		OrderMethod:  database.ByLastAdded,
	}

	// Calculate max page
	nBookmarks, err := h.DB.GetBookmarksCount(searchOptions)
	checkError(err)
	maxPage := int(math.Ceil(float64(nBookmarks) / 30))

	// Fetch all matching bookmarks
	bookmarks, err := h.DB.GetBookmarks(searchOptions)
	checkError(err)

	// Get image URL for each bookmark, and check if it has archive
	for i := range bookmarks {
		strID := strconv.Itoa(bookmarks[i].ID)
		imgPath := fp.Join(h.DataDir, "thumb", strID)
		archivePath := fp.Join(h.DataDir, "archive", strID)

		if fileExists(imgPath) {
			bookmarks[i].ImageURL = path.Join(h.RootPath, "bookmark", strID, "thumb")
		}

		if fileExists(archivePath) {
			bookmarks[i].HasArchive = true
		}
	}

	// Return JSON response
	resp := map[string]interface{}{
		"page":      page,
		"maxPage":   maxPage,
		"bookmarks": bookmarks,
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&resp)
	checkError(err)
}

// apiGetTags is handler for GET /api/tags
func (h *handler) apiGetTags(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Fetch all tags
	tags, err := h.DB.GetTags()
	checkError(err)

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&tags)
	checkError(err)
}

// apiRenameTag is handler for PUT /api/tag
func (h *handler) apiRenameTag(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	tag := model.Tag{}
	err := json.NewDecoder(r.Body).Decode(&tag)
	checkError(err)

	// Update name
	err = h.DB.RenameTag(tag.ID, tag.Name)
	checkError(err)

	fmt.Fprint(w, 1)
}

// apiInsertBookmark is handler for POST /api/bookmark
func (h *handler) apiInsertBookmark(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	book := model.Bookmark{}
	err := json.NewDecoder(r.Body).Decode(&book)
	checkError(err)

	// Create bookmark ID
	book.ID, err = h.DB.CreateNewID("bookmark")
	if err != nil {
		panic(fmt.Errorf("failed to create ID: %v", err))
	}

	// Clean up bookmark URL
	book.URL, err = core.RemoveUTMParams(book.URL)
	if err != nil {
		panic(fmt.Errorf("failed to clean URL: %v", err))
	}

	// Fetch data from internet
	var isFatalErr bool
	content, contentType, err := core.DownloadBookmark(book.URL)
	if err == nil && content != nil {
		request := core.ProcessRequest{
			DataDir:     h.DataDir,
			Bookmark:    book,
			Content:     content,
			ContentType: contentType,
		}

		book, isFatalErr, err = core.ProcessBookmark(request)
		content.Close()

		if err != nil && isFatalErr {
			panic(fmt.Errorf("failed to process bookmark: %v", err))
		}
	}

	// Make sure bookmark's title not empty
	if book.Title == "" {
		book.Title = book.URL
	}

	// Save bookmark to database
	results, err := h.DB.SaveBookmarks(book)
	if err != nil || len(results) == 0 {
		panic(fmt.Errorf("failed to save bookmark: %v", err))
	}
	book = results[0]

	// Return the new bookmark
	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&book)
	checkError(err)
}

// apiDeleteBookmarks is handler for DELETE /api/bookmark
func (h *handler) apiDeleteBookmark(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	ids := []int{}
	err := json.NewDecoder(r.Body).Decode(&ids)
	checkError(err)

	// Delete bookmarks
	err = h.DB.DeleteBookmarks(ids...)
	checkError(err)

	// Delete thumbnail image and archives from local disk
	for _, id := range ids {
		strID := strconv.Itoa(id)
		imgPath := fp.Join(h.DataDir, "thumb", strID)
		archivePath := fp.Join(h.DataDir, "archive", strID)

		os.Remove(imgPath)
		os.Remove(archivePath)
	}

	fmt.Fprint(w, 1)
}

// apiUpdateBookmark is handler for PUT /api/bookmarks
func (h *handler) apiUpdateBookmark(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	request := model.Bookmark{}
	err := json.NewDecoder(r.Body).Decode(&request)
	checkError(err)

	// Validate input
	if request.Title == "" {
		panic(fmt.Errorf("Title must not empty"))
	}

	// Get existing bookmark from database
	filter := database.GetBookmarksOptions{
		IDs:         []int{request.ID},
		WithContent: true,
	}

	bookmarks, err := h.DB.GetBookmarks(filter)
	checkError(err)
	if len(bookmarks) == 0 {
		panic(fmt.Errorf("no bookmark with matching ids"))
	}

	// Set new bookmark data
	book := bookmarks[0]
	book.URL = request.URL
	book.Title = request.Title
	book.Excerpt = request.Excerpt
	book.Public = request.Public

	// Clean up bookmark URL
	book.URL, err = core.RemoveUTMParams(book.URL)
	if err != nil {
		panic(fmt.Errorf("failed to clean URL: %v", err))
	}

	// Set new tags
	for i := range book.Tags {
		book.Tags[i].Deleted = true
	}

	for _, newTag := range request.Tags {
		for i, oldTag := range book.Tags {
			if newTag.Name == oldTag.Name {
				newTag.ID = oldTag.ID
				book.Tags[i].Deleted = false
				break
			}
		}

		if newTag.ID == 0 {
			book.Tags = append(book.Tags, newTag)
		}
	}

	// Update database
	res, err := h.DB.SaveBookmarks(book)
	checkError(err)

	// Add thumbnail image to the saved bookmarks again
	newBook := res[0]
	newBook.ImageURL = request.ImageURL
	newBook.HasArchive = request.HasArchive

	// Return new saved result
	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&newBook)
	checkError(err)
}

// apiUpdateCache is handler for PUT /api/cache
func (h *handler) apiUpdateCache(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	request := struct {
		IDs           []int `json:"ids"`
		KeepMetadata  bool  `json:"keepMetadata"`
		CreateArchive bool  `json:"createArchive"`
	}{}

	err := json.NewDecoder(r.Body).Decode(&request)
	checkError(err)

	// Get existing bookmark from database
	filter := database.GetBookmarksOptions{
		IDs:         request.IDs,
		WithContent: true,
	}

	bookmarks, err := h.DB.GetBookmarks(filter)
	checkError(err)
	if len(bookmarks) == 0 {
		panic(fmt.Errorf("no bookmark with matching ids"))
	}

	// For web interface, let's limit to max 20 IDs to update, and 5 for archival.
	// This is done to prevent the REST request from client took too long to finish.
	if len(bookmarks) > 20 {
		panic(fmt.Errorf("max 20 bookmarks to update"))
	} else if len(bookmarks) > 5 && request.CreateArchive {
		panic(fmt.Errorf("max 5 bookmarks to update with archival"))
	}

	// Fetch data from internet
	mx := sync.RWMutex{}
	wg := sync.WaitGroup{}
	chDone := make(chan struct{})
	chProblem := make(chan int, 10)
	semaphore := make(chan struct{}, 10)

	for i, book := range bookmarks {
		wg.Add(1)

		// Mark whether book will be archived
		book.CreateArchive = request.CreateArchive

		go func(i int, book model.Bookmark, keepMetadata bool) {
			// Make sure to finish the WG
			defer wg.Done()

			// Register goroutine to semaphore
			semaphore <- struct{}{}
			defer func() {
				<-semaphore
			}()

			// Download data from internet
			content, contentType, err := core.DownloadBookmark(book.URL)
			if err != nil {
				chProblem <- book.ID
				return
			}

			request := core.ProcessRequest{
				DataDir:     h.DataDir,
				Bookmark:    book,
				Content:     content,
				ContentType: contentType,
				KeepTitle:   keepMetadata,
				KeepExcerpt: keepMetadata,
			}

			book, _, err = core.ProcessBookmark(request)
			content.Close()

			if err != nil {
				chProblem <- book.ID
				return
			}

			// Update list of bookmarks
			mx.Lock()
			bookmarks[i] = book
			mx.Unlock()
		}(i, book, request.KeepMetadata)
	}

	// Receive all problematic bookmarks
	idWithProblems := []int{}
	go func() {
		for {
			select {
			case <-chDone:
				return
			case id := <-chProblem:
				idWithProblems = append(idWithProblems, id)
			}
		}
	}()

	// Wait until all download finished
	wg.Wait()
	close(chDone)

	// Update database
	_, err = h.DB.SaveBookmarks(bookmarks...)
	checkError(err)

	// Return new saved result
	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&bookmarks)
	checkError(err)
}

// apiUpdateBookmarkTags is handler for PUT /api/bookmarks/tags
func (h *handler) apiUpdateBookmarkTags(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	request := struct {
		IDs  []int       `json:"ids"`
		Tags []model.Tag `json:"tags"`
	}{}

	err := json.NewDecoder(r.Body).Decode(&request)
	checkError(err)

	// Validate input
	if len(request.IDs) == 0 || len(request.Tags) == 0 {
		panic(fmt.Errorf("IDs and tags must not empty"))
	}

	// Get existing bookmark from database
	filter := database.GetBookmarksOptions{
		IDs:         request.IDs,
		WithContent: true,
	}

	bookmarks, err := h.DB.GetBookmarks(filter)
	checkError(err)
	if len(bookmarks) == 0 {
		panic(fmt.Errorf("no bookmark with matching ids"))
	}

	// Set new tags
	for i, book := range bookmarks {
		for _, newTag := range request.Tags {
			for _, oldTag := range book.Tags {
				if newTag.Name == oldTag.Name {
					newTag.ID = oldTag.ID
					break
				}
			}

			if newTag.ID == 0 {
				book.Tags = append(book.Tags, newTag)
			}
		}

		bookmarks[i] = book
	}

	// Update database
	bookmarks, err = h.DB.SaveBookmarks(bookmarks...)
	checkError(err)

	// Get image URL for each bookmark
	for i := range bookmarks {
		strID := strconv.Itoa(bookmarks[i].ID)
		imgPath := fp.Join(h.DataDir, "thumb", strID)
		imgURL := path.Join(h.RootPath, "bookmark", strID, "thumb")

		if fileExists(imgPath) {
			bookmarks[i].ImageURL = imgURL
		}
	}

	// Return new saved result
	err = json.NewEncoder(w).Encode(&bookmarks)
	checkError(err)
}

// apiGetAccounts is handler for GET /api/accounts
func (h *handler) apiGetAccounts(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Get list of usernames from database
	accounts, err := h.DB.GetAccounts(database.GetAccountsOptions{})
	checkError(err)

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(&accounts)
	checkError(err)
}

// apiInsertAccount is handler for POST /api/accounts
func (h *handler) apiInsertAccount(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	var account model.Account
	err := json.NewDecoder(r.Body).Decode(&account)
	checkError(err)

	// Save account to database
	err = h.DB.SaveAccount(account)
	checkError(err)

	fmt.Fprint(w, 1)
}

// apiUpdateAccount is handler for PUT /api/accounts
func (h *handler) apiUpdateAccount(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	request := struct {
		Username    string `json:"username"`
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
		Owner       bool   `json:"owner"`
	}{}

	err := json.NewDecoder(r.Body).Decode(&request)
	checkError(err)

	// Get existing account data from database
	account, exist := h.DB.GetAccount(request.Username)
	if !exist {
		panic(fmt.Errorf("username doesn't exist"))
	}

	// Compare old password with database
	err = bcrypt.CompareHashAndPassword([]byte(account.Password), []byte(request.OldPassword))
	if err != nil {
		panic(fmt.Errorf("old password doesn't match"))
	}

	// Save new password to database
	account.Password = request.NewPassword
	account.Owner = request.Owner
	err = h.DB.SaveAccount(account)
	checkError(err)

	fmt.Fprint(w, 1)
}

// apiDeleteAccount is handler for DELETE /api/accounts
func (h *handler) apiDeleteAccount(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// Decode request
	usernames := []string{}
	err := json.NewDecoder(r.Body).Decode(&usernames)
	checkError(err)

	// Delete accounts
	err = h.DB.DeleteAccounts(usernames...)
	checkError(err)

	fmt.Fprint(w, 1)
}
