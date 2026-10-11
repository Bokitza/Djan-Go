package admin

import (
	"net/url"
	"strconv"

	"github.com/yoav-rogat/djan-go/models"
	"github.com/yoav-rogat/djan-go/query"
)

// ManyToManyField edits the links between a model and a related model that are stored in a join
// model. Create it with ManyToMany and list it in ModelAdmin.ManyToMany.
type ManyToManyField[T any] struct {
	editor extraField
}

// ManyToMany edits rows of the join model J as checkboxes on the form of T:
//
//	admin.ManyToMany("gizbarim", "גזברים",
//		func(link *EconomicUnitGizbar) *models.FK[EconomicUnit] { return &link.EconomicUnit },
//		func(link *EconomicUnitGizbar) *models.FK[Member] { return &link.Member })
//
// The choices are the R rows the user may view in the admin (all rows for superusers when R is not
// registered). Saving only adds or removes links to those rows, so links the user cannot see are kept.
func ManyToMany[T, J, R any](name, label string, owner func(link *J) *models.FK[T], target func(link *J) *models.FK[R]) ManyToManyField[T] {
	return ManyToManyField[T]{editor: &manyToManyEditor[T, J, R]{field: name, label: label, owner: owner, target: target}}
}

// Help sets the text shown under the label.
func (f ManyToManyField[T]) Help(text string) ManyToManyField[T] {
	f.editor.(interface{ setHelp(string) }).setHelp(text)
	return f
}

type manyToManyEditor[T, J, R any] struct {
	field, label, help string
	owner              func(link *J) *models.FK[T]
	target             func(link *J) *models.FK[R]
}

func (e *manyToManyEditor[T, J, R]) setHelp(text string) { e.help = text }

func (e *manyToManyEditor[T, J, R]) name() string { return e.field }

func (e *manyToManyEditor[T, J, R]) links(r *Request, ownerID int64) query.QuerySet[J] {
	return query.Objects[J]().Where(func(link *J) query.Cond { return query.Eq(&e.owner(link).ID, ownerID) })
}

// choices returns the related rows the user may link to, or nil when they may not see any.
func (e *manyToManyEditor[T, J, R]) choices(r *Request) ([]any, *models.ModelInfo, error) {
	related := models.MustInfo[R]()
	queryset := query.ForModel(related)
	if relatedEntry := r.Site.entryFor(related); relatedEntry != nil {
		if !r.Site.permissionsFor(r, relatedEntry).View {
			return nil, related, nil
		}
		queryset = relatedEntry.viewQuerySet(r)
	} else if !r.User.IsSuperuser {
		return nil, related, nil
	}
	objects, err := queryset.All(r.Context())
	return objects, related, err
}

func (e *manyToManyEditor[T, J, R]) render(r *Request, object any, isNew, editable bool, submitted url.Values) (extraView, error) {
	view := extraView{Name: e.field, Label: e.label, Help: e.help, Widget: "checkboxes", ReadOnly: !editable}
	checked := map[int64]bool{}
	if !isNew {
		links, err := e.links(r, models.MustInfo[T]().PKOf(object)).All(r.Context())
		if err != nil {
			return view, err
		}
		for _, link := range links {
			checked[e.target(link).ID] = true
		}
	}
	if submitted != nil {
		checked = map[int64]bool{}
		for _, id := range selectedIDs(submitted, e.field) {
			checked[id] = true
		}
	}
	objects, related, err := e.choices(r)
	if err == nil {
		err = loadLabels(r.Context(), related, objects)
	}
	if err != nil {
		return view, err
	}
	group := extraOptionGroup{}
	for _, choice := range objects {
		id := related.PKOf(choice)
		option := extraOption{Value: strconv.FormatInt(id, 10), Label: related.DisplayString(choice), Checked: checked[id]}
		if option.Checked {
			view.Count++
		}
		group.Options = append(group.Options, option)
	}
	if len(group.Options) > 0 {
		view.Groups = []extraOptionGroup{group}
	}
	return view, nil
}

func (e *manyToManyEditor[T, J, R]) validate(*Request, bool, url.Values) string { return "" }

func (e *manyToManyEditor[T, J, R]) save(r *Request, object any, form url.Values) error {
	objects, related, err := e.choices(r)
	if err != nil || len(objects) == 0 {
		return err
	}
	visible := make([]int64, len(objects))
	for index, choice := range objects {
		visible[index] = related.PKOf(choice)
	}
	ctx := r.Context()
	ownerID := models.MustInfo[T]().PKOf(object)
	_, err = e.links(r, ownerID).Where(func(link *J) query.Cond { return query.In(&e.target(link).ID, visible...) }).Delete(ctx)
	if err != nil {
		return err
	}
	allowed := map[int64]bool{}
	for _, id := range visible {
		allowed[id] = true
	}
	for _, id := range selectedIDs(form, e.field) {
		if !allowed[id] {
			continue
		}
		link := models.New[J]()
		*e.owner(link) = models.Ref[T](ownerID)
		*e.target(link) = models.Ref[R](id)
		if err := query.Create(ctx, link); err != nil {
			return err
		}
	}
	return nil
}
