package cmd

import (
	"context"
	gphotos "github.com/gphotosuploader/google-photos-api-client-go/v2"
	"github.com/gphotosuploader/google-photos-api-client-go/v2/uploader/resumable"
	"github.com/gphotosuploader/gphotos-uploader-cli/internal/app"
	"github.com/gphotosuploader/gphotos-uploader-cli/internal/cmd/flags"
	"github.com/gphotosuploader/gphotos-uploader-cli/internal/filter"
	"github.com/gphotosuploader/gphotos-uploader-cli/internal/log"
	"github.com/gphotosuploader/gphotos-uploader-cli/internal/upload"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
	"google.golang.org/api/googleapi"
	"net/http"
	"regexp"
)

var (
	requestQuotaErrorRe = regexp.MustCompile(`Quota exceeded for quota metric 'All requests' and limit 'All requests per day'`)
)

// PushCmd holds the required data for the push cmd
type PushCmd struct {
	*flags.GlobalFlags

	// command flags
	DryRunMode bool
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

	pushCmd.Flags().BoolVar(&cmd.DryRunMode, "dry-run", false, "Dry run mode")

	return pushCmd
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

	photosService, err := newPhotosService(cli.Client, cli.UploadSessionTracker, cli.Logger)
	if err != nil {
		return err
	}

	if cmd.DryRunMode {
		cli.Logger.Info("[DRY-RUN] Running in dry run mode. No changes will be made.")
	}

	// launch all folder upload jobs
	for _, config := range cli.Config.Jobs {
		sourceFolder := config.SourceFolder

		filterFiles, err := filter.Compile(config.IncludePatterns, config.ExcludePatterns)
		if err != nil {
			return err
		}

		folder := upload.UploadFolderJob{
			FileTracker: cli.FileTracker,

			SourceFolder: sourceFolder,
			CreateAlbums: config.CreateAlbums,
			Filter:       filterFiles,
		}

		// get UploadItem{} to be uploaded to Google Photos.
		itemsToUpload, err := folder.ScanFolder(cli.Logger)
		if err != nil {
			cli.Logger.Fatalf("Failed to process location '%s': %s", config.SourceFolder, err)
			continue
		}

		totalItems := len(itemsToUpload)
		var uploadedItems int

		cli.Logger.Infof("Found %d items to be uploaded processing location '%s'.", totalItems, config.SourceFolder)

		bar := progressbar.NewOptions(totalItems,
			progressbar.OptionFullWidth(),
			progressbar.OptionSetDescription("Uploading files..."),
			progressbar.OptionSetPredictTime(false),
			progressbar.OptionShowCount(),
			progressbar.OptionSetVisibility(!cmd.Debug),
		)

		itemsGroupedByAlbum := upload.GroupByAlbum(itemsToUpload)
		for albumName, files := range itemsGroupedByAlbum {
			albumId, err := getOrCreateAlbum(ctx, photosService.Albums, albumName)
			if err != nil {
				cli.Logger.Failf("Unable to create album '%s': %s", albumName, err)
				continue
			}

			for _, file := range files {
				cli.Logger.Debugf("Processing (%d/%d): %s", uploadedItems+1, totalItems, file)

				if !cmd.DryRunMode {
					// Upload the file and add it to PhotosService.
					_, err := photosService.UploadFileToAlbum(ctx, albumId, file.Path)
					if err != nil {
						if googleApiErr, ok := err.(*googleapi.Error); ok {
							if requestQuotaErrorRe.MatchString(googleApiErr.Message) {
								cli.Logger.Failf("returning 'quota exceeded' error")
								return err
							}
						} else {
							cli.Logger.Failf("Error processing %s", file)
							continue
						}
					}

					// Mark the file as uploaded in the FileTracker.
					if err := cli.FileTracker.Put(file.Path); err != nil {
						cli.Logger.Warnf("Tracking file as uploaded failed: file=%s, error=%v", file, err)
					}

					if config.DeleteAfterUpload {
						if err := file.Remove(); err != nil {
							cli.Logger.Errorf("Deletion request failed: file=%s, err=%v", file, err)
						}
					}
				}

				_ = bar.Add(1)
				uploadedItems++
			}
		}

		_ = bar.Finish()

		cli.Logger.Donef("%d processed files: %d successfully, %d with errors", totalItems, uploadedItems, totalItems-uploadedItems)
	}
	return nil
}

func newPhotosService(client *http.Client, sessionTracker app.UploadSessionTracker, logger log.Logger) (*gphotos.Client, error) {
	u, err := resumable.NewResumableUploader(client, sessionTracker, resumable.WithLogger(logger))
	if err != nil {
		return nil, err
	}
	return gphotos.NewClient(client, gphotos.WithUploader(u))
}

// getOrCreateAlbum returns the created (or existent) album in PhotosService.
func getOrCreateAlbum(ctx context.Context, service gphotos.AlbumsService, title string) (string, error) {
	// Returns if empty to avoid a PhotosService call.
	if title == "" {
		return "", nil
	}

	if album, err := service.GetByTitle(ctx, title); err == nil {
		return album.ID, nil
	}

	album, err := service.Create(ctx, title)
	if err != nil {
		return "", err
	}

	return album.ID, nil
}
