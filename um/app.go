package um

import (
	"bytes"
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/csrf"
	"github.com/gorilla/mux"
	minio "github.com/minio/minio-go"
	uuid "github.com/satori/go.uuid"
	"golang.org/x/crypto/scrypt"
)

var templates *template.Template

func init() {
	templates = template.Must(template.ParseGlob("templates/*.html"))
}

type App struct {
	DB                    *sql.DB
	ResourceStorageClient *minio.Client
	MaxAllowedImgSize     int64
	BucketName            string
	PresignedURLExpiry    time.Duration
}

func (a *App) Shutdown() {
	err := a.DB.Close()
	if err != nil {
		log.Printf("failed to close db %v\n", err)
	}
}

func (a *App) IndexHandler(w http.ResponseWriter, r *http.Request) {
	// serve form to upload a single image with caption
	p := map[string]interface{}{
		"Title":          "Ummage",
		"Headline":       "Upload Image",
		"Information":    fmt.Sprintf("Max allowed upload size is %dmb", a.MaxAllowedImgSize/(1024*1024)),
		csrf.TemplateTag: csrf.TemplateField(r),
	}

	templates.ExecuteTemplate(w, "index.html", p)
}

func (a *App) UploadFileHandler(w http.ResponseWriter, r *http.Request) {
	var f Resource

	u := uuid.NewV4()
	f.UUID = u.String()

	r.Body = http.MaxBytesReader(w, r.Body, a.MaxAllowedImgSize)
	err := r.ParseMultipartForm(a.MaxAllowedImgSize)
	if err != nil {
		fmt.Fprintf(w, "Can not handle images bigger than %dmb, failed with %s", a.MaxAllowedImgSize/(1024*1024), err)
		return
	}

	file, handler, err := r.FormFile("resource")
	if err != nil {
		fmt.Fprintf(w, "Can not handle images bigger than %dmb, failed with %s", a.MaxAllowedImgSize/(1024*1024), err)
		return
	}
	defer file.Close()

	destructKey := r.FormValue("destruct-key")

	if len(destructKey) != 0 {
		destructKeySalt, err := GetRandomBytes(32)
		if err != nil {
			fmt.Fprintf(w, "Internal server error %s", err)
			return
		}
		hash, _ := scrypt.Key([]byte(destructKey), destructKeySalt, 32768, 8, 1, 32)

		f.DestructKey = hash
		f.DestructKeySalt = destructKeySalt
	}

	caption := r.FormValue("caption")
	if len(caption) != 0 {
		f.Caption = strings.TrimSpace(caption)
	}

	fileType := handler.Header.Get("Content-Type")
	if !strings.HasPrefix(fileType, "image/") {
		fmt.Fprintf(w, "Could not determine the file type %s, accepted types are image/*\n", fileType)
		return
	}

	f.ContentType = fileType

	userMetadata := map[string]string{
		"name": handler.Filename,
	}

	f.Name = handler.Filename

	_, err = a.ResourceStorageClient.PutObjectWithContext(r.Context(), a.BucketName, u.String(), file, handler.Size, minio.PutObjectOptions{UserMetadata: userMetadata, ContentType: fileType, CacheControl: "private, max-age=-1, no-cache, no-store, must-revalidate", ContentDisposition: handler.Header.Get("Content-Disposition")})

	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	stmt, err := a.DB.Prepare(`INSERT INTO resource(uuid, name, caption, content_type, destruct_key, destruct_key_salt) values(?,?,?,?,?,?)`)
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}
	defer stmt.Close()

	_, err = stmt.Exec(f.UUID, f.Name, f.Caption, f.ContentType, f.DestructKey, f.DestructKeySalt)
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	// show upload response page
	p := map[string]interface{}{
		"Title":   "Ummage",
		"Caption": f.Caption,
		"Uuid":    f.UUID,
	}

	templates.ExecuteTemplate(w, "upload-response.html", p)
}

func (a *App) ViewFileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	stmt, err := a.DB.Prepare("select name, caption, content_type from resource where uuid = ?")
	if err != nil {
		fmt.Fprintf(w, "error is %s", err)
		return
	}

	var f Resource

	uuid := vars["id"]
	row := stmt.QueryRow(uuid)
	err = row.Scan(&f.Name, &f.Caption, &f.ContentType)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Requested image not found %s", err)
		return
	}

	reqParams := make(url.Values) // TODO: give all the goodies of image serving
	reqParams.Set("response-content-disposition", "attachment; filename=\\"+f.Name+"\"")
	reqParams.Set("Cache-Control", "private, max-age=-1, no-cache, no-store, must-revalidate")

	// Generates a presigned url which expires in a day.
	uploadedAt, err := a.ResourceStorageClient.PresignedGetObject(a.BucketName, uuid, a.PresignedURLExpiry, reqParams)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	p := map[string]interface{}{
		"Title":          "Ummage",
		"Caption":        f.Caption,
		"Src":            uploadedAt,
		"Uuid":           uuid,
		csrf.TemplateTag: csrf.TemplateField(r),
	}

	templates.ExecuteTemplate(w, "view.html", p)
}

func (a *App) DeleteFileViewHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	uuid := vars["id"]

	stmt, err := a.DB.Prepare("select caption from resource where uuid = ?")
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	var caption string
	row := stmt.QueryRow(uuid)
	err = row.Scan(&caption)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Requested image not found %s", err)
		return
	}

	p := map[string]interface{}{
		"Title":          "Ummage",
		"Caption":        caption,
		"Uuid":           uuid,
		csrf.TemplateTag: csrf.TemplateField(r),
	}

	templates.ExecuteTemplate(w, "destruct.html", p)
}

func (a *App) DeleteFileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	destructKey := r.FormValue("destruct-key")
	if len(destructKey) == 0 {
		fmt.Fprintf(w, "Please retry with a non empty destruct key")
		return
	}

	stmt, err := a.DB.Prepare("select destruct_key, destruct_key_salt from resource where uuid = ?")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}

	var f Resource

	uuid := vars["id"]

	row := stmt.QueryRow(uuid)
	err = row.Scan(&f.DestructKey, &f.DestructKeySalt)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "Requested image not found %s", err)
		return
	}

	dk, err := scrypt.Key([]byte(destructKey), f.DestructKeySalt, 32768, 8, 1, 32)
	if err != nil {
		fmt.Fprintf(w, "Internal server error %s", err)
		return
	}
	if bytes.Equal(dk, f.DestructKey) {
		stmt, err := a.DB.Prepare("DELETE FROM resource WHERE uuid = ?;")
		if err != nil {
			fmt.Fprintf(w, "Internal server error %s", err)
			return
		}
		defer stmt.Close()
		_, err = stmt.Exec(uuid)
		if err != nil {
			fmt.Fprintf(w, "Internal server error %s", err)
			return
		}
		// delete the file
		err = a.ResourceStorageClient.RemoveObject(a.BucketName, uuid)
		if err == nil {
			fmt.Fprintf(w, "Image is deleted")
			return
		}
	} else {
		fmt.Fprintf(w, "Possible wrong credentials, please try again.")
		return
	}
}
