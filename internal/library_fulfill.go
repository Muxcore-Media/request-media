package internal

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	booksv1 "github.com/Muxcore-Media/media-books/proto/gen/muxcore/books/v1"
	audiobooksv1 "github.com/Muxcore-Media/media-audiobooks/proto/gen/muxcore/audiobooks/v1"
	comicsv1 "github.com/Muxcore-Media/media-comics/proto/gen/muxcore/comics/v1"
	requestmedia "github.com/Muxcore-Media/request-media/proto/requestmedia"
)

func (m *Module) RequestBook(ctx context.Context, req *requestmedia.RequestBookRequest) (*requestmedia.RequestBookResponse, error) {
	if err := m.requireRequestRoles(ctx); err != nil {
		return nil, err
	}
	ctx = incomingContextWithUser(ctx, req.GetRequestedBy())
	requestedBy := requestedByFromContext(ctx, req.GetRequestedBy())
	roles := rolesFromContext(ctx)
	tenantID := m.resolveTenant(ctx, nil)
	requestID := fmt.Sprintf("req_book_%d", time.Now().UnixNano())

	if m.needsApproval(requestedBy, roles) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "book", Title: req.GetTitle(), Year: req.GetYear(),
			Status: "pending", RequestedBy: requestedBy, TenantID: tenantID,
			ArtistName: strings.TrimSpace(req.GetAuthorName()),
		})
		return &requestmedia.RequestBookResponse{RequestId: requestID, Status: "pending"}, nil
	}

	bookID, status, err := m.fulfillRequest(ctx, fulfillParams{
		RequestID: requestID, ItemType: "book", Title: req.GetTitle(), Year: req.GetYear(),
		ArtistName: req.GetAuthorName(), RequestedBy: requestedBy, TenantID: tenantID,
		ISBN: req.GetIsbn(),
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestBookResponse{
		RequestId: requestID, BookId: bookID, Status: status,
	}, nil
}

func (m *Module) RequestComic(ctx context.Context, req *requestmedia.RequestComicRequest) (*requestmedia.RequestComicResponse, error) {
	if err := m.requireRequestRoles(ctx); err != nil {
		return nil, err
	}
	ctx = incomingContextWithUser(ctx, req.GetRequestedBy())
	requestedBy := requestedByFromContext(ctx, req.GetRequestedBy())
	roles := rolesFromContext(ctx)
	tenantID := m.resolveTenant(ctx, nil)
	requestID := fmt.Sprintf("req_comic_%d", time.Now().UnixNano())

	if m.needsApproval(requestedBy, roles) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "comic", Title: req.GetTitle(),
			Status: "pending", RequestedBy: requestedBy, TenantID: tenantID,
		})
		return &requestmedia.RequestComicResponse{RequestId: requestID, Status: "pending"}, nil
	}

	seriesID, status, err := m.fulfillRequest(ctx, fulfillParams{
		RequestID: requestID, ItemType: "comic", Title: req.GetTitle(),
		Publisher: req.GetPublisher(), ComicVineID: req.GetComicvineId(),
		RequestedBy: requestedBy, TenantID: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestComicResponse{
		RequestId: requestID, SeriesId: seriesID, Status: status,
	}, nil
}

func (m *Module) RequestAudiobook(ctx context.Context, req *requestmedia.RequestAudiobookRequest) (*requestmedia.RequestAudiobookResponse, error) {
	if err := m.requireRequestRoles(ctx); err != nil {
		return nil, err
	}
	ctx = incomingContextWithUser(ctx, req.GetRequestedBy())
	requestedBy := requestedByFromContext(ctx, req.GetRequestedBy())
	roles := rolesFromContext(ctx)
	tenantID := m.resolveTenant(ctx, nil)
	requestID := fmt.Sprintf("req_audio_%d", time.Now().UnixNano())

	if m.needsApproval(requestedBy, roles) {
		m.saveRequest(&requestRecord{
			ID: requestID, ItemType: "audiobook", Title: req.GetTitle(), Year: req.GetYear(),
			Status: "pending", RequestedBy: requestedBy, TenantID: tenantID,
			ArtistName: strings.TrimSpace(req.GetAuthorName()),
		})
		return &requestmedia.RequestAudiobookResponse{RequestId: requestID, Status: "pending"}, nil
	}

	bookID, status, err := m.fulfillRequest(ctx, fulfillParams{
		RequestID: requestID, ItemType: "audiobook", Title: req.GetTitle(), Year: req.GetYear(),
		ArtistName: req.GetAuthorName(), Narrator: req.GetNarrator(), ASIN: req.GetAsin(),
		RequestedBy: requestedBy, TenantID: tenantID,
	})
	if err != nil {
		return nil, err
	}
	return &requestmedia.RequestAudiobookResponse{
		RequestId: requestID, AudiobookId: bookID, Status: status,
	}, nil
}

func (m *Module) fulfillBook(ctx context.Context, p fulfillParams) (bookID, authorID, status string, err error) {
	recBase := func(status, itemID string) *requestRecord {
		rec := &requestRecord{
			ID: p.RequestID, ItemType: "book", ItemID: itemID, Title: p.Title, Year: p.Year,
			Status: status, RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy, TenantID: p.TenantID,
			ArtistName: p.ArtistName,
		}
		if !p.PreserveCreate.IsZero() {
			rec.CreatedAt = p.PreserveCreate
		}
		if p.ApprovedBy != "" {
			rec.ApprovedAt = time.Now().UTC()
		}
		return rec
	}

	addr, err := m.findModuleAddr(ctx, "media.library.books")
	if err != nil {
		m.saveRequest(recBase("requested", ""))
		slog.Info("book requested (library unavailable)", "title", p.Title)
		return "", "", "requested", nil
	}
	conn, err := dialModuleGRPC(addr)
	if err != nil {
		return "", "", "", fmt.Errorf("dial media-books: %w", err)
	}
	defer func() { _ = conn.Close() }()
	client := booksv1.NewBookManagementServiceClient(conn)

	authorName := strings.TrimSpace(p.ArtistName)
	if authorName == "" {
		authorName = "Unknown Author"
	}
	authResp, err := client.AddAuthor(ctx, &booksv1.AddAuthorRequest{Name: authorName, Monitored: true})
	if err != nil {
		return "", "", "", fmt.Errorf("add author: %w", err)
	}
	authorID = authResp.GetAuthor().GetId()
	bookResp, err := client.AddBook(ctx, &booksv1.AddBookRequest{
		AuthorId: authorID, Title: p.Title, Isbn: p.ISBN, Year: p.Year, Monitored: true,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("add book: %w", err)
	}
	bookID = bookResp.GetBook().GetId()
	m.saveRequest(recBase("added", bookID))
	slog.Info("book requested", "title", p.Title, "book_id", bookID)
	return bookID, authorID, "added", nil
}

func (m *Module) fulfillComic(ctx context.Context, p fulfillParams) (seriesID, status string, err error) {
	recBase := func(status, itemID string) *requestRecord {
		rec := &requestRecord{
			ID: p.RequestID, ItemType: "comic", ItemID: itemID, Title: p.Title, Status: status,
			RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy, TenantID: p.TenantID,
		}
		if !p.PreserveCreate.IsZero() {
			rec.CreatedAt = p.PreserveCreate
		}
		if p.ApprovedBy != "" {
			rec.ApprovedAt = time.Now().UTC()
		}
		return rec
	}

	addr, err := m.findModuleAddr(ctx, "media.library.comics")
	if err != nil {
		m.saveRequest(recBase("requested", ""))
		slog.Info("comic requested (library unavailable)", "title", p.Title)
		return "", "requested", nil
	}
	conn, err := dialModuleGRPC(addr)
	if err != nil {
		return "", "", fmt.Errorf("dial media-comics: %w", err)
	}
	defer func() { _ = conn.Close() }()
	client := comicsv1.NewComicManagementServiceClient(conn)
	resp, err := client.AddSeries(ctx, &comicsv1.AddSeriesRequest{
		Title: p.Title, Publisher: p.Publisher, ComicvineId: p.ComicVineID, Monitored: true,
	})
	if err != nil {
		return "", "", fmt.Errorf("add comic series: %w", err)
	}
	seriesID = resp.GetSeries().GetId()
	m.saveRequest(recBase("added", seriesID))
	slog.Info("comic requested", "title", p.Title, "series_id", seriesID)
	return seriesID, "added", nil
}

func (m *Module) fulfillAudiobook(ctx context.Context, p fulfillParams) (audiobookID, authorID, status string, err error) {
	recBase := func(status, itemID string) *requestRecord {
		rec := &requestRecord{
			ID: p.RequestID, ItemType: "audiobook", ItemID: itemID, Title: p.Title, Year: p.Year,
			Status: status, RequestedBy: p.RequestedBy, ApprovedBy: p.ApprovedBy, TenantID: p.TenantID,
			ArtistName: p.ArtistName,
		}
		if !p.PreserveCreate.IsZero() {
			rec.CreatedAt = p.PreserveCreate
		}
		if p.ApprovedBy != "" {
			rec.ApprovedAt = time.Now().UTC()
		}
		return rec
	}

	addr, err := m.findModuleAddr(ctx, "media.library.audiobooks")
	if err != nil {
		m.saveRequest(recBase("requested", ""))
		slog.Info("audiobook requested (library unavailable)", "title", p.Title)
		return "", "", "requested", nil
	}
	conn, err := dialModuleGRPC(addr)
	if err != nil {
		return "", "", "", fmt.Errorf("dial media-audiobooks: %w", err)
	}
	defer func() { _ = conn.Close() }()
	client := audiobooksv1.NewAudiobookManagementServiceClient(conn)

	authorName := strings.TrimSpace(p.ArtistName)
	if authorName == "" {
		authorName = "Unknown Author"
	}
	authResp, err := client.AddAuthor(ctx, &audiobooksv1.AddAuthorRequest{Name: authorName, Monitored: true})
	if err != nil {
		return "", "", "", fmt.Errorf("add author: %w", err)
	}
	authorID = authResp.GetAuthor().GetId()
	bookResp, err := client.AddAudiobook(ctx, &audiobooksv1.AddAudiobookRequest{
		AuthorId: authorID, Title: p.Title, Narrator: p.Narrator, Asin: p.ASIN,
		Year: p.Year, Monitored: true,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("add audiobook: %w", err)
	}
	audiobookID = bookResp.GetAudiobook().GetId()
	m.saveRequest(recBase("added", audiobookID))
	slog.Info("audiobook requested", "title", p.Title, "audiobook_id", audiobookID)
	return audiobookID, authorID, "added", nil
}
