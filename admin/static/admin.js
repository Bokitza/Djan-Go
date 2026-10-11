document.addEventListener('alpine:init', () => {
  Alpine.store('theme', {
    dark: document.documentElement.classList.contains('dark'),
    toggle() {
      this.dark = !this.dark
      document.documentElement.classList.toggle('dark', this.dark)
      localStorage.theme = this.dark ? 'dark' : 'light'
    },
  })

  Alpine.store('confirm', {
    open: false,
    title: '',
    message: '',
    action: '',
    button: 'Delete',
    ids: [],
    ask(options) {
      Object.assign(this, { ids: [], button: 'Delete' }, options, { open: true })
    },
  })

  Alpine.data('toasts', (initial) => ({
    items: [],
    init() {
      ;(initial || []).forEach((toast) => this.push(toast))
      document.body.addEventListener('toast', (event) => this.push(event.detail))
    },
    push(toast) {
      const id = Math.random().toString(36).slice(2)
      this.items.push({ id, level: toast.Level || toast.level || 'info', message: toast.Message || toast.message })
      setTimeout(() => this.dismiss(id), 5000)
    },
    dismiss(id) {
      this.items = this.items.filter((item) => item.id !== id)
    },
  }))

  const score = (text, search) => {
    text = text.toLowerCase()
    const index = text.indexOf(search)
    if (index >= 0) return 1000 - index
    let position = 0
    for (const character of search) {
      position = text.indexOf(character, position)
      if (position < 0) return 0
      position++
    }
    return 1
  }

  Alpine.data('palette', (items) => ({
    open: false,
    search: '',
    active: 0,
    items,
    get results() {
      const search = this.search.toLowerCase().trim()
      if (!search) return this.items
      return this.items
        .map((item) => ({ item, score: score(item.label + ' ' + item.hint, search) }))
        .filter((entry) => entry.score > 0)
        .sort((a, b) => b.score - a.score)
        .map((entry) => entry.item)
    },
    show() {
      this.open = true
      this.search = ''
      this.active = 0
      this.$nextTick(() => this.$refs.input.focus())
    },
    move(delta) {
      const count = this.results.length
      if (!count) return
      this.active = (this.active + delta + count) % count
      this.$nextTick(() => this.$refs.list.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' }))
    },
    run(item) {
      item = item || this.results[this.active]
      if (!item) return
      this.open = false
      if (item.action === 'theme') Alpine.store('theme').toggle()
      else if (item.action === 'logout') document.getElementById('logout-form').submit()
      else window.location.href = item.url
    },
  }))

  Alpine.data('selection', () => ({
    selected: [],
    boxes() {
      return [...this.$root.querySelectorAll('input[data-row]')]
    },
    toggleAll(event) {
      this.selected = event.target.checked ? this.boxes().map((box) => box.value) : []
    },
    allChecked() {
      const boxes = this.boxes()
      return boxes.length > 0 && this.selected.length === boxes.length
    },
    clear() {
      this.selected = []
    },
  }))

  Alpine.data('combobox', (value, label) => ({
    value,
    label,
    open: false,
    choose(element) {
      this.value = element.dataset.id
      this.label = element.dataset.label
      this.open = false
    },
    clear() {
      this.value = ''
      this.label = ''
    },
  }))
})

document.addEventListener('keydown', (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    window.dispatchEvent(new CustomEvent('open-palette'))
  }
})
