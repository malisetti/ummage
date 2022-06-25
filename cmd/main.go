package main

// upload img
// choose deletion criteria
// option to delete using a password
// view img
import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"ummage/um"

	"github.com/gorilla/csrf"
	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
	minio "github.com/minio/minio-go"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/sync/errgroup"
)

const (
	presignedURLExpiry = time.Second * 1
	maxAllowedImgSize  = 1 << 20 // bytes
	bucketName         = "resources"
)

const (
	endpoint        = "localhost:9000"
	accessKeyID     = "AKIAIOSFODNN7EXAMPLE"
	secretAccessKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	location        = "us-east-1"
)

const domain = "tickr.xyz"

var (
	dir    string
	secure bool

	authKey     []byte
	minioClient *minio.Client
	db          *sql.DB
)

func init() {
	var g errgroup.Group

	g.Go(func() (err error) {
		flag.StringVar(&dir, "dir", ".", "the directory to serve files from. Defaults to the current dir")
		flag.BoolVar(&secure, "secure", false, "weather the app is running on tls or not")
		// minioClient, err = um.SetupStorage(secure, dir, endpoint, accessKeyID, secretAccessKey, bucketName, location)
		return
	})

	g.Go(func() (err error) {
		authKey, err = um.GetRandomBytes(32)
		return
	})

	g.Go(func() (err error) {
		db, err = um.SetupDB("./app.db")
		return
	})

	err := g.Wait()
	if err != nil {
		panic(err)
	}
}

func main() {
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	certManager := autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache("certs"),
	}

	a := &um.App{
		ResourceStorageClient: minioClient,
		DB:                    db,
		MaxAllowedImgSize:     maxAllowedImgSize,
		BucketName:            bucketName,
		PresignedURLExpiry:    presignedURLExpiry,
	}

	defer a.Shutdown()

	CSRF := csrf.Protect(authKey, csrf.FieldName("ummage-csrf"), csrf.CookieName("ummage-cookie"), csrf.Secure(secure))

	r := mux.NewRouter()
	r.HandleFunc("/", a.IndexHandler).Methods("GET")
	r.HandleFunc("/", a.UploadFileHandler).Methods("POST")

	r.HandleFunc("/i/{id}", a.ViewFileHandler).Methods("GET")

	r.HandleFunc("/i/{id}/destruct", a.DeleteFileViewHandler).Methods("GET")
	r.HandleFunc("/i/{id}/destruct", a.DeleteFileHandler).Methods("POST")

	tlsConfig := certManager.TLSConfig()
	tlsConfig.GetCertificate = um.GetSelfSignedOrLetsEncryptCert(&certManager)

	server := http.Server{
		Addr:      ":443",
		Handler:   CSRF(r),
		TLSConfig: tlsConfig,
	}

	go http.ListenAndServe(":80", http.HandlerFunc(um.RedirectHTTP))

	log.Printf("Server listening on %s", server.Addr)
	go func() {
		if err := server.ListenAndServeTLS("", ""); err != nil {
			fmt.Println(err)
		}
	}()
	<-done

	log.Println("Server Stopped")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() {
		// extra handling here
		cancel()
	}()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server Shutdown Failed:%+v", err)
	}
	log.Println("Server Exited Properly")
}
