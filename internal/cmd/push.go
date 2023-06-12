package cmd

import (
	"context"
	"errors"
	"fmt"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v2"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/albums"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/uploader/resumable"
	"github.com/gphotosuploader/gphotos-uploader-cli/internal/app"
	"github.com/gphotosuploader/gphotos-uploader-cli/internal/cmd/flags"
	logp "github.com/gphotosuploader/gphotos-uploader-cli/internal/log"
	"github.com/spf13/cobra"
	"google.golang.org/api/googleapi"
	"log"
	"net/http"
	"net/http/httputil"
	"regexp"
)

var (
	requestQuotaErrorRe = regexp.MustCompile(`Quota exceeded for quota metric 'All requests' and limit 'All requests per day'`)
)

// PushCmd holds the required data for the push cmd
type PushCmd struct {
	*flags.GlobalFlags

	// command flags
	NumberOfWorkers int
	DryRunMode      bool
	ConsumeMode     bool
}

func NewPushCmd(globalFlags *flags.GlobalFlags) *cobra.Command {
	cmd := &PushCmd{GlobalFlags: globalFlags}

	pushCmd := &cobra.Command{
		Use:   "push",
		Short: "Push local files to Google Photos service",
		Long:  `Scan configured folders in the configuration and push all new object to Google Photos service.`,
		Args:  cobra.NoArgs,
		RunE:  cmd.Run,
	}

	pushCmd.Flags().IntVar(&cmd.NumberOfWorkers, "workers", 1, "Number of workers")
	pushCmd.Flags().BoolVar(&cmd.DryRunMode, "dry-run", false, "Dry run mode")
	pushCmd.Flags().BoolVar(&cmd.ConsumeMode, "consume", false, "Consume reqs")

	return pushCmd
}

type loggingTransport struct {
	transport http.RoundTripper
}

func (t loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil {
		//dump, err := httputil.DumpRequestOut(req, false)
		//if err != nil {
		//	log.Printf("Could not dump request: %s", err)
		//}
		log.Printf("[REQUEST] %s %s", req.Method, req.URL)
	}
	res, err := t.transport.RoundTrip(req)
	if res != nil {
		//dump, err := httputil.DumpResponse(res, false)

		b, err := httputil.DumpResponse(res, true)

		if err != nil {
			log.Printf("Could not dump response: %s", err)
		}
		log.Printf("[RESPONSE] %s %s\n%s", req.Method, req.URL, b)
	}
	return res, err
}

func (cmd *PushCmd) Run(cobraCmd *cobra.Command, args []string) error {
	ctx := context.Background()
	cli, err := app.Start(ctx, cmd.CfgDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = cli.Stop()
	}()

	cli.Logger.Info("[DEV] This is a development version. Please be warned that it's not ready for production")

	// TODO: We are going to use a map in memory to keep the Album's collection, so we could skip
	// using CachedAlbums in the Google Photos service.
	photosService, err := newPhotosService(cli.Client, cli.UploadSessionTracker, cli.Logger)
	if err != nil {
		return err
	}

	// Get all the albums from Google Photos (our new cache)
	//albumCache, err := photosService.Albums.List(ctx)
	//if err != nil {
	//	return err
	//}

	//cli.Logger.Infof("Found & cached %d albums.", len(albumCache))

	fmt.Println(cmd.ConsumeMode)

	if cmd.ConsumeMode {

		cli.Logger.Infof("Consume 5000 reqs")

		for i := 0; i < 5000; i++ {
			_, err := photosService.Albums.List(ctx)
			if err != nil {
				if googleApiErr, ok := err.(*googleapi.Error); ok {
					if requestQuotaErrorRe.MatchString(googleApiErr.Message) {
						cli.Logger.Failf("Daily quota exceeded: waiting 12h until quota is recovered: err=%v", googleApiErr)
						fmt.Printf("Google error: %v", googleApiErr)
						return err
					}
					fmt.Printf("Google error: %v", googleApiErr)
				} else {
					cli.Logger.Failf("Error")
					fmt.Printf("Error: %v", err)
					continue
				}
			}
		}
	}

	cli.Logger.Debug("Second client")

	cli.Client.Transport = loggingTransport{cli.Client.Transport}

	photosService2, err := newPhotosService(cli.Client, cli.UploadSessionTracker, cli.Logger)
	if err != nil {
		return err
	}

	for i := 0; i < 100; i++ {
		albumName := fmt.Sprintf("auto-generate-%d", i)
		album, err := photosService2.Albums.Create(ctx, albumName)
		if err != nil {
			if googleApiErr, ok := err.(*googleapi.Error); ok {
				if requestQuotaErrorRe.MatchString(googleApiErr.Message) {
					cli.Logger.Failf("Daily quota exceeded: waiting 12h until quota is recovered: err=%v", googleApiErr)
					fmt.Printf("Google error: %v", googleApiErr)
					return err
				}
				fmt.Printf("Google error: %v", googleApiErr)
			} else {
				cli.Logger.Failf("Error processing %s", albumName)
				fmt.Printf("Error: %v", err)
				continue
			}
		}
		cli.Logger.Debugf("Created album [%s]: %s", albumName, album.ID)
	}

	// launch all folder upload jobs
	//for _, config := range cli.Config.Jobs {
	//	sourceFolder := config.SourceFolder
	//
	//	filterFiles, err := filter.Compile(config.IncludePatterns, config.ExcludePatterns)
	//	if err != nil {
	//		return err
	//	}
	//
	//	folder := upload.UploadFolderJob{
	//		FileTracker: cli.FileTracker,
	//
	//		SourceFolder: sourceFolder,
	//		CreateAlbums: config.CreateAlbums,
	//		Filter:       filterFiles,
	//	}
	//
	//	// get UploadItem{} to be uploaded to Google Photos.
	//	itemsToUpload, err := folder.ScanFolder(cli.Logger)
	//	if err != nil {
	//		cli.Logger.Fatalf("Failed to process location '%s': %s", config.SourceFolder, err)
	//		continue
	//	}
	//
	//	totalItems := len(itemsToUpload)
	//	var uploadedItems int
	//
	//	cli.Logger.Infof("Found %d items to be uploaded processing location '%s'.", totalItems, config.SourceFolder)
	//
	//	bar := progressbar.NewOptions(totalItems,
	//		progressbar.OptionFullWidth(),
	//		progressbar.OptionSetDescription("Uploading files..."),
	//		progressbar.OptionSetPredictTime(false),
	//		progressbar.OptionShowCount(),
	//		progressbar.OptionSetVisibility(!cmd.Debug),
	//	)
	//
	//	for _, item := range itemsToUpload {
	//		albumId, err := getOrCreateAlbum(ctx, photosService.Albums, albumCache, item.AlbumName)
	//		if err != nil {
	//			cli.Logger.Failf("Unable to create album '%s': %s", item.AlbumName, err)
	//			continue
	//		}
	//
	//		cli.Logger.Debugf("Processing (%d/%d): %s", uploadedItems+1, totalItems, item)
	//
	//		if !cmd.DryRunMode {
	//			// Upload the file and add it to PhotosService.
	//			_, err := photosService.UploadFileToAlbum(ctx, albumId, item.Path)
	//			if err != nil {
	//				if googleApiErr, ok := err.(*googleapi.Error); ok {
	//					if requestQuotaErrorRe.MatchString(googleApiErr.Message) {
	//						cli.Logger.Failf("Daily quota exceeded: waiting 12h until quota is recovered")
	//						time.Sleep(12 * time.Hour)
	//						continue
	//					}
	//				} else {
	//					cli.Logger.Failf("Error processing %s", item)
	//					continue
	//				}
	//			}
	//
	//			// Mark the file as uploaded in the FileTracker.
	//			if err := cli.FileTracker.Put(item.Path); err != nil {
	//				cli.Logger.Warnf("Tracking file as uploaded failed: file=%s, error=%v", item, err)
	//			}
	//
	//			if config.DeleteAfterUpload {
	//				if err := item.Remove(); err != nil {
	//					cli.Logger.Errorf("Deletion request failed: file=%s, err=%v", item, err)
	//				}
	//			}
	//		}
	//
	//		_ = bar.Add(1)
	//		uploadedItems++
	//	}
	//
	//	_ = bar.Finish()
	//
	//	cli.Logger.Donef("%d processed files: %d successfully, %d with errors", totalItems, uploadedItems, totalItems-uploadedItems)
	//}
	return nil
}

func newPhotosService(client *http.Client, sessionTracker app.UploadSessionTracker, logger logp.Logger) (*gphotos.Client, error) {
	u, err := resumable.NewResumableUploader(client, sessionTracker, resumable.WithLogger(logger))
	if err != nil {
		return nil, err
	}
	return gphotos.NewClient(client, gphotos.WithUploader(u))
}

func getAlbumFromCollection(collection []albums.Album, title string) (albumId string, err error) {
	for _, album := range collection {
		if album.Title == title {
			return album.ID, nil
		}
	}
	return "", errors.New("album not found")
}

func getOrCreateAlbum(ctx context.Context, service gphotos.AlbumsService, albumsCache []albums.Album, title string) (string, error) {
	// Returns if empty to avoid a PhotosService call.
	if title == "" {
		return "", nil
	}

	if albumId, err := getAlbumFromCollection(albumsCache, title); err == nil {
		return albumId, nil
	}

	album, err := service.Create(ctx, title)
	if err != nil {
		return "", err
	}

	albumsCache = append(albumsCache, *album)

	return album.ID, nil
}
