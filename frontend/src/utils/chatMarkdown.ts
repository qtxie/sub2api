import DOMPurify from 'dompurify'
import katex from 'katex'
import { Marked, type MarkedExtension, type Tokens } from 'marked'

interface MathToken extends Tokens.Generic {
  type: 'inlineMath' | 'displayMath'
  raw: string
  text: string
  displayMode: boolean
}

function renderMath(token: MathToken): string {
  try {
    return katex.renderToString(token.text, {
      displayMode: token.displayMode,
      throwOnError: false,
      trust: false
    })
  } catch {
    return token.raw
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
  }
}

const mathExtension: MarkedExtension = {
  extensions: [
    {
      name: 'displayMath',
      level: 'block',
      start(src) {
        const bracketIndex = src.indexOf('\\[')
        const dollarIndex = src.indexOf('$$')
        if (bracketIndex < 0) return dollarIndex < 0 ? undefined : dollarIndex
        if (dollarIndex < 0) return bracketIndex
        return Math.min(bracketIndex, dollarIndex)
      },
      tokenizer(src) {
        const match = /^(?:\\\[([\s\S]+?)\\\]|\$\$([\s\S]+?)\$\$)/.exec(src)
        if (!match) return undefined
        return {
          type: 'displayMath',
          raw: match[0],
          text: match[1] ?? match[2],
          displayMode: true
        } satisfies MathToken
      },
      renderer(token) {
        return renderMath(token as MathToken)
      }
    },
    {
      name: 'inlineMath',
      level: 'inline',
      start(src) {
        const index = src.indexOf('\\(')
        return index < 0 ? undefined : index
      },
      tokenizer(src) {
        const match = /^\\\(([\s\S]+?)\\\)/.exec(src)
        if (!match) return undefined
        return {
          type: 'inlineMath',
          raw: match[0],
          text: match[1],
          displayMode: false
        } satisfies MathToken
      },
      renderer(token) {
        return renderMath(token as MathToken)
      }
    }
  ]
}

const chatMarkdown = new Marked(mathExtension)

export function renderChatMarkdown(content: string): string {
  const html = chatMarkdown.parse(content || '', { async: false }) as string
  return DOMPurify.sanitize(html)
}
