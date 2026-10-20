package idform

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

// WrapDataSource returns d keeping every configured id (an attribute named id,
// *_id or *_ids) in the spelling the configuration wrote it (`id = "<uuid>"` read back as a public id): Terraform
// refuses a data source result that changes a configured value.
func WrapDataSource(d datasource.DataSource) datasource.DataSource {
	return &wrappedDataSource{inner: d}
}

type wrappedDataSource struct{ inner datasource.DataSource }

var _ datasource.DataSourceWithConfigure = (*wrappedDataSource)(nil)

func (w *wrappedDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	w.inner.Metadata(ctx, req, resp)
}

func (w *wrappedDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	w.inner.Schema(ctx, req, resp)
}

func (w *wrappedDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if c, ok := w.inner.(datasource.DataSourceWithConfigure); ok {
		c.Configure(ctx, req, resp)
	}
}

func (w *wrappedDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	w.inner.Read(ctx, req, resp)
	if !resp.State.Raw.IsNull() {
		resp.State.Raw = keepPrior(resp.State.Raw, req.Config.Raw, rules{loose, idName}, nil)
	}
}
