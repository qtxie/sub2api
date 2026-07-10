import { describe, expect, it } from 'vitest'

import { renderChatMarkdown } from '@/utils/chatMarkdown'

describe('renderChatMarkdown', () => {
  it('renders boxed inline LaTeX instead of exposing the command', () => {
    const html = renderChatMarkdown(String.raw`所以答案确实是 \(\boxed{21}\)。`)
    const container = document.createElement('div')
    container.innerHTML = html

    const renderedMath = container.querySelector('.katex-html')
    expect(renderedMath).not.toBeNull()
    expect(renderedMath?.textContent).toContain('21')
    expect(renderedMath?.textContent).not.toContain('\\boxed')
  })

  it('renders bracket and double-dollar display math', () => {
    const bracketHtml = renderChatMarkdown(String.raw`Result: \[\frac{1}{2}\] exactly.`)
    const dollarHtml = renderChatMarkdown('$$x^2$$')

    expect(bracketHtml).toContain('class="katex-display"')
    expect(bracketHtml).toContain('exactly.')
    expect(dollarHtml).toContain('class="katex-display"')
  })

  it('leaves LaTeX-looking content inside code spans untouched', () => {
    const html = renderChatMarkdown('Use `\\(\\boxed{21}\\)` literally.')

    expect(html).toContain('<code>\\(\\boxed{21}\\)</code>')
    expect(html).not.toContain('class="katex"')
  })

  it('sanitizes unsafe HTML after rendering math', () => {
    const html = renderChatMarkdown(String.raw`<img src="x" onerror="alert(1)"> \(x+1\)`)

    expect(html).not.toContain('onerror')
    expect(html).toContain('class="katex"')
  })
})
