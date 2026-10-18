const commands = {
  bold: { run: (chain) => chain.toggleBold(), active: ['bold'] },
  italic: { run: (chain) => chain.toggleItalic(), active: ['italic'] },
  underline: { run: (chain) => chain.toggleUnderline(), active: ['underline'] },
  strike: { run: (chain) => chain.toggleStrike(), active: ['strike'] },
  heading2: { run: (chain) => chain.toggleHeading({ level: 2 }), active: ['heading', { level: 2 }] },
  heading3: { run: (chain) => chain.toggleHeading({ level: 3 }), active: ['heading', { level: 3 }] },
  bulletList: { run: (chain) => chain.toggleBulletList(), active: ['bulletList'] },
  orderedList: { run: (chain) => chain.toggleOrderedList(), active: ['orderedList'] },
  blockquote: { run: (chain) => chain.toggleBlockquote(), active: ['blockquote'] },
  codeBlock: { run: (chain) => chain.toggleCodeBlock(), active: ['codeBlock'] },
  link: { active: ['link'] },
  undo: { run: (chain) => chain.undo() },
  redo: { run: (chain) => chain.redo() },
}

class RichTextEditor extends HTMLElement {
  async connectedCallback() {
    if (this.editor) return
    const { Editor, StarterKit } = await import(this.dataset.tiptap)
    const input = this.querySelector('input[type="hidden"]')
    const buttons = [...this.querySelectorAll('[data-command]')]
    const refresh = () => {
      for (const button of buttons) {
        const active = commands[button.dataset.command]?.active
        button.classList.toggle('is-active', Boolean(active && this.editor.isActive(...active)))
      }
    }
    this.editor = new Editor({
      element: this.querySelector('[data-editor]'),
      extensions: [StarterKit.configure({ link: { openOnClick: false, autolink: true } })],
      content: input.value,
      editorProps: { attributes: { class: 'rich-text rich-text-content' } },
      onUpdate: ({ editor }) => {
        input.value = editor.isEmpty ? '' : editor.getHTML()
      },
      onTransaction: refresh,
    })
    for (const button of buttons) {
      button.addEventListener('click', () => this.run(button.dataset.command))
    }
    refresh()
  }

  run(name) {
    const chain = this.editor.chain().focus()
    if (name === 'link') {
      const previous = this.editor.getAttributes('link').href || ''
      const href = window.prompt(this.dataset.linkPrompt || 'URL', previous)
      if (href === null) return
      if (href.trim() === '') chain.extendMarkRange('link').unsetLink().run()
      else chain.extendMarkRange('link').setLink({ href: href.trim() }).run()
      return
    }
    commands[name]?.run(chain).run()
  }

  disconnectedCallback() {
    this.editor?.destroy()
    this.editor = null
  }
}

if (!customElements.get('rich-text-editor')) customElements.define('rich-text-editor', RichTextEditor)
