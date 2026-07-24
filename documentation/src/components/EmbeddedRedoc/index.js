import React, {useEffect, useRef, useState} from 'react';
import BrowserOnly from '@docusaurus/BrowserOnly';
import useBaseUrl from '@docusaurus/useBaseUrl';
import {useColorMode} from '@docusaurus/theme-common';

const REDOC_SCRIPT_ID = 'embedded-redoc-script';
const REDOC_SCRIPT_URL =
  'https://cdn.redocly.com/redoc/v2.5.3/bundles/redoc.standalone.js';
const REDOC_SCRIPT_INTEGRITY =
  'sha384-xiEssMQFSpSfLbzRZCGfxxIM5QDb2DTrU6vyoZdp2sV1L6pmOMy6MpTtUoLbpC96';

const redocDarkTheme = {
  colors: {
    primary: {
      main: '#68d4ad',
      light: '#91e0c3',
      dark: '#2fab82',
      contrastText: '#08110d',
    },
    text: {
      primary: '#e7f0ec',
      secondary: '#a9bbb3',
    },
    border: {
      dark: '#3a4b43',
      light: '#26352e',
    },
    success: {main: '#68d4ad'},
    warning: {main: '#f2cc60'},
    error: {main: '#ff7b72'},
  },
  sidebar: {
    backgroundColor: '#111513',
    textColor: '#b8c9c1',
    activeTextColor: '#68d4ad',
  },
  rightPanel: {
    backgroundColor: '#0b0f0d',
    textColor: '#e7f0ec',
  },
  schema: {
    nestedBackground: '#181e1b',
    linesColor: '#3a4b43',
    typeNameColor: '#91e0c3',
    typeTitleColor: '#e7f0ec',
    requireLabelColor: '#ff7b72',
  },
  codeBlock: {
    backgroundColor: '#0b0f0d',
  },
  typography: {
    fontFamily: 'Inter, ui-sans-serif, system-ui, -apple-system, sans-serif',
    headings: {
      fontFamily: 'Inter, ui-sans-serif, system-ui, -apple-system, sans-serif',
    },
    heading1: {color: '#f2f7f5'},
    heading2: {color: '#e7f0ec'},
    heading3: {color: '#d8e5df'},
    code: {
      color: '#91e0c3',
      backgroundColor: '#181e1b',
    },
    links: {
      color: '#68d4ad',
      visited: '#83dcbc',
      hover: '#bbead9',
    },
  },
};

const redocOptions = {
  menuToggle: true,
  nativeScrollbars: true,
  schemasExpansionLevel: 1,
  scrollYOffset: () =>
    document.querySelector('.navbar')?.getBoundingClientRect().height ?? 0,
  showExtensions: true,
  sortOperationsAlphabetically: true,
};

function loadRedoc() {
  if (window.Redoc) {
    return Promise.resolve(window.Redoc);
  }

  return new Promise((resolve, reject) => {
    const existingScript = document.getElementById(REDOC_SCRIPT_ID);
    const script = existingScript ?? document.createElement('script');

    script.addEventListener('load', () => resolve(window.Redoc), {once: true});
    script.addEventListener(
      'error',
      () => reject(new Error('Unable to load the Redoc renderer.')),
      {once: true},
    );

    if (!existingScript) {
      script.id = REDOC_SCRIPT_ID;
      script.src = REDOC_SCRIPT_URL;
      script.integrity = REDOC_SCRIPT_INTEGRITY;
      script.crossOrigin = 'anonymous';
      document.head.appendChild(script);
    }
  });
}

function EmbeddedRedocClient() {
  const containerRef = useRef(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const {colorMode} = useColorMode();
  const specificationUrl = useBaseUrl('/openapi/swagger.json');

  useEffect(() => {
    let active = true;
    setError('');
    setLoading(true);

    loadRedoc()
      .then((redoc) => {
        if (!active || !containerRef.current) {
          return;
        }

        redoc.init(
          specificationUrl,
          {
            ...redocOptions,
            ...(colorMode === 'dark' ? {theme: redocDarkTheme} : {}),
          },
          containerRef.current,
          (initError) => {
            if (!active) {
              return;
            }
            if (initError) {
              setError(
                initError instanceof Error ? initError.message : String(initError),
              );
              return;
            }
            setLoading(false);
          },
        );
      })
      .catch((value) => {
        if (active) {
          setError(value instanceof Error ? value.message : String(value));
        }
      });

    return () => {
      active = false;
      if (containerRef.current) {
        containerRef.current.replaceChildren();
      }
    };
  }, [colorMode, specificationUrl]);

  if (error) {
    return <div className="alert alert--danger">{error}</div>;
  }

  return (
    <div className="redoclyEmbeddedReference">
      {loading && (
        <div className="redoclyLoading">Loading API documentation…</div>
      )}
      <div className="redoclyEmbeddedContent" ref={containerRef} />
    </div>
  );
}

export default function EmbeddedRedoc() {
  return (
    <BrowserOnly
      fallback={
        <div className="redoclyEmbeddedReference">
          <div className="redoclyLoading">Loading API documentation…</div>
        </div>
      }>
      {() => <EmbeddedRedocClient />}
    </BrowserOnly>
  );
}
