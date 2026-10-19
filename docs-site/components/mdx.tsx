import defaultMdxComponents from 'fumadocs-ui/mdx';
import { Callout } from 'fumadocs-ui/components/callout';
import { Tab, Tabs } from 'fumadocs-ui/components/tabs';
import { Step, Steps } from 'fumadocs-ui/components/steps';
import { Card, Cards } from 'fumadocs-ui/components/card';
import { Accordion, Accordions } from 'fumadocs-ui/components/accordion';
import { File, Files, Folder } from 'fumadocs-ui/components/files';
import { TypeTable } from 'fumadocs-ui/components/type-table';
import type { ImgHTMLAttributes } from 'react';
import type { MDXComponents } from 'mdx/types';

const DefaultImageComponent = defaultMdxComponents.img;

function getImagePath(src: unknown): string | undefined {
  if (typeof src === 'string') return src;
  if (!src || typeof src !== 'object') return undefined;
  if ('src' in src && typeof src.src === 'string') return src.src;

  const defaultExport = 'default' in src ? src.default : undefined;
  if (
    defaultExport &&
    typeof defaultExport === 'object' &&
    'src' in defaultExport &&
    typeof defaultExport.src === 'string'
  ) {
    return defaultExport.src;
  }

  return undefined;
}

function MdxImage(props: ImgHTMLAttributes<HTMLImageElement>) {
  const src = getImagePath(props.src);
  if (!src || !/\.gif(?:$|[?#])/i.test(src)) {
    return <DefaultImageComponent {...props} />;
  }

  // The deployed Next image optimizer returns 404 for GIFs. Serve them directly
  // so the browser loads and animates the original asset.
  return <img {...props} src={src} className={props.className ? `rounded-lg ${props.className}` : 'rounded-lg'} />;
}

export function getMDXComponents(components?: MDXComponents): MDXComponents {
  return {
    ...defaultMdxComponents,
    img: MdxImage,
    Callout,
    Tabs,
    Tab,
    Steps,
    Step,
    Cards,
    Card,
    Accordions,
    Accordion,
    Files,
    Folder,
    File,
    TypeTable,
    ...components,
  };
}
