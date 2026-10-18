import 'mathlive/fonts.css';

import { mergeAttributes, Node as TipTapNode } from '@tiptap/core';
import { TableKit } from '@tiptap/extension-table';
import { EditorContent, NodeViewWrapper, ReactNodeViewRenderer, useEditor, useEditorState, type Editor, type NodeViewProps } from '@tiptap/react';
import StarterKit from '@tiptap/starter-kit';
import katex from 'katex';
import { MathfieldElement } from 'mathlive';
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react';
import { createPortal } from 'react-dom';

import { ApiError, describe } from '../api/client';
import { parse, serialize, type Node } from './format';
import { figureUrl } from './RichText';

// A visual editor for question text: teachers select and click (or use
// Ctrl+B/I/U), insert math in a Desmos-like field (type / for a fraction,
// ^ for a power, sqrt for a root), and paste or upload images. It reads and
// writes the stored text format (./format.ts), so nothing else changes.

// MathLive's fonts come bundled with the page (fonts.css above); no sounds.
MathfieldElement.fontsDirectory = null;
MathfieldElement.soundsDirectory = null;

// ---- Uploading ----

export async function uploadImage(file: File): Promise<string> {
  const body = new FormData();
  body.append('file', file);
  let res: Response;
  try {
    res = await fetch('/v1/admin/practice/images', { method: 'POST', body, credentials: 'same-origin', headers: { 'X-MakonBook-Admin': '1' } });
  } catch {
    throw new ApiError(0, 'network', 'Can’t reach MakonBook. Check the connection and try again.');
  }
  const json = (await res.json().catch(() => ({}))) as { key?: string; error?: { code?: string; message?: string } };
  if (!res.ok || !json.key) throw new ApiError(res.status, json.error?.code ?? 'unknown', json.error?.message ?? 'Couldn’t upload the image.');
  return json.key;
}

// ---- Math ----

function MathPopover({
  anchorRef,
  latex,
  display,
  onDone,
  onCancel,
}: {
  anchorRef: RefObject<HTMLElement | null>;
  latex: string;
  display: boolean;
  onDone: (latex: string, display: boolean) => void;
  onCancel: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const field = useRef<MathfieldElement | null>(null);
  const [own, setOwn] = useState(display);
  const done = useRef(onDone);
  const cancel = useRef(onCancel);
  useEffect(() => {
    done.current = onDone;
    cancel.current = onCancel;
  });
  const ownRef = useRef(own);
  useEffect(() => {
    ownRef.current = own;
  }, [own]);

  useEffect(() => {
    const mf = new MathfieldElement();
    mf.value = latex;
    mf.mathVirtualKeyboardPolicy = 'manual';
    mf.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        done.current(mf.value, ownRef.current);
      } else if (e.key === 'Escape') {
        e.preventDefault();
        cancel.current();
      }
    });
    host.current?.appendChild(mf);
    field.current = mf;
    const t = window.setTimeout(() => mf.focus(), 0);
    return () => {
      window.clearTimeout(t);
      window.mathVirtualKeyboard?.hide();
      mf.remove();
    };
    // The field is made once; its value is read when closing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Below the math it edits.
  const [pos, setPos] = useState({ left: 100, top: 100 });
  useLayoutEffect(() => {
    const r = anchorRef.current?.getBoundingClientRect();
    if (r) setPos({ left: Math.max(8, Math.min(r.left - 8, window.innerWidth - 440)), top: r.bottom + 8 });
  }, [anchorRef]);
  const { left, top } = pos;
  return createPortal(
    <div className="math-pop" style={{ left, top }} role="dialog" aria-label="Edit math" onMouseDown={(e) => e.stopPropagation()}>
      <div ref={host} className="math-pop-field" />
      <div className="small faint">
        Type <b>/</b> for a fraction, <b>^</b> for a power, <b>sqrt</b> for a root, <b>pi</b> for π. Enter to finish.
      </div>
      <div className="toolbar" style={{ justifyContent: 'space-between' }}>
        <span className="toolbar">
          <label className="check small">
            <input type="checkbox" checked={own} onChange={(e) => setOwn(e.target.checked)} /> On its own line
          </label>
          <button type="button" className="btn secondary small" onClick={() => {
              const kb = window.mathVirtualKeyboard;
              field.current?.focus();
              if (kb.visible) kb.hide();
              else kb.show({ animate: true });
            }}>
            Keyboard
          </button>
        </span>
        <span className="toolbar">
          <button type="button" className="btn secondary small" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className="btn small" onClick={() => onDone(field.current?.value ?? latex, own)}>
            Done
          </button>
        </span>
      </div>
    </div>,
    document.body,
  );
}

function MathView({ node, updateAttributes, deleteNode, editor, selected, getPos }: NodeViewProps) {
  const latex = String(node.attrs.latex ?? '');
  const display = !!node.attrs.display;
  const [open, setOpen] = useState(latex === '');
  const ref = useRef<HTMLSpanElement>(null);
  const html = useMemo(
    () => katex.renderToString(latex.trim() || '\\square', { output: 'mathml', displayMode: display, throwOnError: false, strict: false }),
    [latex, display],
  );
  return (
    <NodeViewWrapper as="span" className={`math-node${display ? ' display' : ''}${selected ? ' selected' : ''}`}>
      <span
        ref={ref}
        title="Click to edit"
        onClick={() => {
          if (!editor.isEditable) return;
          // Put the cursor after the math, so nothing typed or clicked meanwhile replaces it.
          const pos = getPos();
          if (typeof pos === 'number') editor.commands.setTextSelection(pos + node.nodeSize);
          setOpen(true);
        }}
        dangerouslySetInnerHTML={{ __html: html }}
      />
      {open && (
        <MathPopover
          anchorRef={ref}
          latex={latex}
          display={display}
          onDone={(value, disp) => {
            setOpen(false);
            if (!value.trim()) deleteNode();
            else updateAttributes({ latex: value, display: disp });
            editor.commands.focus();
          }}
          onCancel={() => {
            setOpen(false);
            if (!latex.trim()) deleteNode();
            editor.commands.focus();
          }}
        />
      )}
    </NodeViewWrapper>
  );
}

const MathNode = TipTapNode.create({
  name: 'math',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: true,
  addAttributes() {
    return {
      latex: { default: '', parseHTML: (el) => el.getAttribute('data-latex') ?? '', renderHTML: (a) => ({ 'data-latex': a.latex }) },
      display: { default: false, parseHTML: (el) => el.hasAttribute('data-display'), renderHTML: (a) => (a.display ? { 'data-display': '' } : {}) },
    };
  },
  parseHTML: () => [{ tag: 'span[data-math]' }],
  renderHTML: ({ HTMLAttributes }) => ['span', mergeAttributes(HTMLAttributes, { 'data-math': '' })],
  addNodeView: () => ReactNodeViewRenderer(MathView, { stopEvent: () => false }),
  addKeyboardShortcuts() {
    return { 'Mod-m': () => this.editor.commands.insertContent({ type: 'math', attrs: { latex: '' } }) };
  },
});

// ---- Figures ----

function FigureView({ node, updateAttributes, deleteNode, selected }: NodeViewProps) {
  const alt = String(node.attrs.alt ?? '');
  return (
    <NodeViewWrapper className={`figure-node${selected ? ' selected' : ''}`} contentEditable={false}>
      <img src={figureUrl(String(node.attrs.key))} alt={alt} draggable={false} />
      <div className="figure-node-bar">
        <input
          className="input"
          value={alt}
          placeholder="Describe the image (what it shows), for students who can’t see it"
          aria-label="Image description"
          aria-invalid={!alt.trim()}
          onChange={(e) => updateAttributes({ alt: e.target.value })}
        />
        <button type="button" className="btn danger small" onClick={deleteNode}>
          Remove
        </button>
      </div>
    </NodeViewWrapper>
  );
}

const FigureNode = TipTapNode.create({
  name: 'figure',
  group: 'block',
  atom: true,
  draggable: true,
  addAttributes() {
    return {
      key: { default: '', parseHTML: (el) => el.getAttribute('data-key') ?? '', renderHTML: (a) => ({ 'data-key': a.key }) },
      alt: { default: '', parseHTML: (el) => el.getAttribute('data-alt') ?? '', renderHTML: (a) => ({ 'data-alt': a.alt }) },
    };
  },
  parseHTML: () => [{ tag: 'figure[data-key]' }],
  renderHTML: ({ HTMLAttributes }) => ['figure', mergeAttributes(HTMLAttributes)],
  addNodeView: () =>
    ReactNodeViewRenderer(FigureView, {
      // Typing the description belongs to the input, not to the document.
      stopEvent: ({ event }) => (event.target as HTMLElement | null)?.tagName === 'INPUT',
    }),
});

// ---- Toolbar ----

function Btn({ on, disabled, title, onClick, children }: { on?: boolean; disabled?: boolean; title: string; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      className={`tb${on ? ' on' : ''}`}
      title={title}
      aria-label={title}
      aria-pressed={on}
      disabled={disabled}
      // Keep the selection in the text while clicking.
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}>
      {children}
    </button>
  );
}

function Toolbar({ editor, images, rich, onImage, source, onSource }: { editor: Editor; images: boolean; rich: boolean; onImage: () => void; source: boolean; onSource: () => void }) {
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      bold: e.isActive('bold'),
      italic: e.isActive('italic'),
      underline: e.isActive('underline'),
      list: e.isActive('bulletList'),
      table: e.isActive('table'),
      undo: e.can().undo(),
      redo: e.can().redo(),
    }),
  });
  const c = () => editor.chain().focus();
  return (
    <div className="tbar" role="toolbar" aria-label="Formatting">
      {!source && (
        <>
          <Btn title="Bold (Ctrl+B)" on={s.bold} onClick={() => c().toggleBold().run()}>
            <b>B</b>
          </Btn>
          <Btn title="Italic (Ctrl+I)" on={s.italic} onClick={() => c().toggleItalic().run()}>
            <i>I</i>
          </Btn>
          <Btn title="Underline (Ctrl+U)" on={s.underline} onClick={() => c().toggleUnderline().run()}>
            <u>U</u>
          </Btn>
          <span className="tsep" />
          <Btn title="Math: fractions, powers, roots… (Ctrl+M)" onClick={() => c().insertContent({ type: 'math', attrs: { latex: '' } }).run()}>
            <span className="fx">√x</span>
          </Btn>
          <Btn title="Blank to fill: ______" onClick={() => c().insertContent('______').run()}>
            ___
          </Btn>
          {images && (
            <Btn title="Image (you can also paste a screenshot)" onClick={onImage}>
              🖼
            </Btn>
          )}
          {rich && (
            <>
              <Btn title="Bulleted list" on={s.list} onClick={() => c().toggleBulletList().run()}>
                ••
              </Btn>
              <Btn title="Table" on={s.table} onClick={() => c().insertTable({ rows: 3, cols: 2, withHeaderRow: true }).run()}>
                ▦
              </Btn>
            </>
          )}
          {s.table && (
            <span className="tgroup">
              <Btn title="Add a row below" onClick={() => c().addRowAfter().run()}>
                +row
              </Btn>
              <Btn title="Add a column to the right" onClick={() => c().addColumnAfter().run()}>
                +col
              </Btn>
              <Btn title="Delete this row" onClick={() => c().deleteRow().run()}>
                −row
              </Btn>
              <Btn title="Delete this column" onClick={() => c().deleteColumn().run()}>
                −col
              </Btn>
              <Btn title="Delete the table" onClick={() => c().deleteTable().run()}>
                ✕▦
              </Btn>
            </span>
          )}
          <span className="tsep" />
          <Btn title="Undo (Ctrl+Z)" disabled={!s.undo} onClick={() => c().undo().run()}>
            ↶
          </Btn>
          <Btn title="Redo (Ctrl+Shift+Z)" disabled={!s.redo} onClick={() => c().redo().run()}>
            ↷
          </Btn>
        </>
      )}
      <span style={{ marginLeft: 'auto' }} />
      <Btn title={source ? 'Back to the visual editor' : 'Show the text code (for experts)'} on={source} onClick={onSource}>
        {'</>'}
      </Btn>
    </div>
  );
}

// ---- The editor ----

export function RichEditor({
  value,
  onChange,
  images = true,
  rich = true,
  compact = false,
  label,
  invalid,
}: {
  value: string;
  onChange: (value: string) => void;
  images?: boolean; // figures allowed here (not in explanations)
  rich?: boolean; // lists and tables
  compact?: boolean; // a choice: one short line, toolbar when focused
  label: string;
  invalid?: boolean;
}) {
  const emitted = useRef(value);
  const [source, setSource] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const editorRef = useRef<Editor | null>(null);

  async function insertImage(file: File) {
    const ed = editorRef.current;
    if (!ed || !images) return;
    setUploadError(null);
    setUploading(true);
    try {
      const key = await uploadImage(file);
      ed.chain().focus().insertContent({ type: 'figure', attrs: { key, alt: '' } }).run();
    } catch (e) {
      setUploadError(describe(e));
    } finally {
      setUploading(false);
    }
  }
  const insertImageRef = useRef(insertImage);
  useEffect(() => {
    insertImageRef.current = insertImage;
  });

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        heading: false,
        blockquote: false,
        codeBlock: false,
        code: false,
        strike: false,
        orderedList: false,
        horizontalRule: false,
        link: false,
        bulletList: rich ? {} : false,
        listItem: rich ? {} : false,
        listKeymap: rich ? {} : false,
      }),
      ...(rich ? [TableKit.configure({ table: { resizable: false } })] : []),
      MathNode,
      FigureNode,
    ],
    content: parse(value),
    editorProps: {
      attributes: { class: `rte-body${compact ? ' compact' : ''}`, 'aria-label': label, role: 'textbox', 'aria-multiline': 'true' },
      // A pasted or dropped screenshot becomes an uploaded figure.
      handlePaste: (_view, event) => {
        const file = Array.from(event.clipboardData?.files ?? []).find((f) => f.type.startsWith('image/'));
        if (!file || !images) return false;
        void insertImageRef.current(file);
        return true;
      },
      handleDrop: (_view, event) => {
        const file = Array.from((event as DragEvent).dataTransfer?.files ?? []).find((f) => f.type.startsWith('image/'));
        if (!file || !images) return false;
        void insertImageRef.current(file);
        return true;
      },
    },
    onUpdate: ({ editor: e }) => {
      const text = serialize(e.getJSON() as Node);
      emitted.current = text;
      onChange(text);
    },
  });
  useEffect(() => {
    editorRef.current = editor;
  }, [editor]);

  // Changes from outside (undo all, loading an old version, the code view).
  useEffect(() => {
    if (editor && value !== emitted.current) {
      emitted.current = value;
      editor.commands.setContent(parse(value), { emitUpdate: false });
    }
  }, [value, editor]);

  const focused = useEditorState({ editor, selector: ({ editor: e }) => e?.isFocused ?? false });
  const [hovering, setHovering] = useState(false);
  const showBar = !compact || focused || hovering || source;

  if (!editor) return null;
  return (
    <div className={`rte${invalid ? ' invalid' : ''}${compact ? ' compact' : ''}`} onMouseEnter={() => setHovering(true)} onMouseLeave={() => setHovering(false)}>
      {showBar && (
        <Toolbar
          editor={editor}
          images={images}
          rich={rich}
          source={source}
          onImage={() => fileInput.current?.click()}
          onSource={() => setSource((s) => !s)}
        />
      )}
      {source ? (
        <textarea className="textarea rte-source" rows={compact ? 2 : 8} value={value} aria-label={`${label} (text code)`} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <EditorContent editor={editor} />
      )}
      {uploading && <div className="small muted">Uploading the image…</div>}
      {uploadError && <div className="field-error">{uploadError}</div>}
      {images && (
        <input
          ref={fileInput}
          type="file"
          accept="image/png,image/jpeg,image/gif,image/webp"
          hidden
          onChange={(e) => {
            const f = e.target.files?.[0];
            e.target.value = '';
            if (f) void insertImage(f);
          }}
        />
      )}
    </div>
  );
}
