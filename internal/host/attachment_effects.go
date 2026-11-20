package host

import (
	"context"
	"regexp"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var dataURLPrefix = regexp.MustCompile(`^data:[^,]*,`)

// written saves a staged image for sending. One that carries no marks is already on disk; one that
// does arrives with the marks already drawn into its source, so only the file needs writing.
func written(ctx context.Context, attachment *domain.OutgoingAttachment, desktop RuntimeDesktop) (*domain.RunAttachment, error) {
	var path string
	if attachment.Path != nil && len(attachment.Annotations) == 0 {
		path = *attachment.Path
	} else {
		saved, err := desktop.SaveAttachment(ctx, dataURLPrefix.ReplaceAllLiteralString(attachment.Source, ""), attachment.Path)
		if err != nil {
			return nil, err
		}
		path = saved
	}
	labels := []string{}
	for _, annotation := range attachment.Annotations {
		if annotation.Kind == domain.ImageAnnotationKindBox {
			labels = append(labels, annotation.Text)
		}
	}
	return &domain.RunAttachment{Path: path, Labels: labels, Context: attachment.Context}, nil
}

// all runs work for every item at once and answers with every result in order, or the first failure.
func all[T, R any](items []T, work func(item T) (R, error)) ([]R, error) {
	results := make([]R, len(items))
	var (
		wait  sync.WaitGroup
		once  sync.Once
		first error
	)
	for index, item := range items {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := work(item)
			if err != nil {
				once.Do(func() { first = err })
				return
			}
			results[index] = result
		}()
	}
	wait.Wait()
	return results, first
}

// AttachmentEffects write the composer's images on their way out, which the message they ride waits for.
var AttachmentEffects = EffectHandlers{
	"send-attachments": handle(func(ctx context.Context, effect application.WorkspaceEffectSendAttachments, host EffectHost) error {
		saved, err := all(effect.Attachments, func(attachment *domain.OutgoingAttachment) (*domain.RunAttachment, error) {
			return written(ctx, attachment, host.Desktop)
		})
		if err == nil {
			ids := make([]string, len(effect.Attachments))
			for index, attachment := range effect.Attachments {
				ids[index] = attachment.ID
			}
			event := application.AttachmentEventAttachmentsSaved{TaskID: effect.TaskID, IDs: ids, Attachments: saved}
			if effect.Steer != nil && *effect.Steer {
				event.Steer = effect.Steer
			}
			if effect.Text != nil && *effect.Text != "" {
				event.Text = effect.Text
			}
			err = host.Dispatch(ctx, event)
		}
		if err != nil {
			return host.Dispatch(ctx, application.AttachmentEventAttachmentsFailed{TaskID: effect.TaskID, Message: errorMessage(err)})
		}
		return nil
	}),
}

var AttachmentHost = HostModule{Effects: AttachmentEffects}
