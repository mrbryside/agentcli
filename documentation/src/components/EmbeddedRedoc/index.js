import React, {useEffect, useMemo, useRef, useState} from 'react';
import useBaseUrl from '@docusaurus/useBaseUrl';
import {useColorMode} from '@docusaurus/theme-common';

const REDOC_SCRIPT_ID = 'embedded-redoc-script';
const REDOC_SCRIPT_URL =
  'https://cdn.redocly.com/redoc/v2.5.3/bundles/redoc.standalone.js';
const REDOC_SCRIPT_INTEGRITY =
  'sha384-xiEssMQFSpSfLbzRZCGfxxIM5QDb2DTrU6vyoZdp2sV1L6pmOMy6MpTtUoLbpC96';

const redocPalettes = {
  light: {
    background: '#ffffff',
    surface: '#f5f6f7',
    code: '#0b0f0d',
    codeText: '#e7f0ec',
    primary: '#207f68',
    primaryLight: '#2aa686',
    primaryDark: '#165949',
    text: '#1c1e21',
    textMuted: '#5f6368',
    border: '#dadde1',
    warning: '#a66b00',
    error: '#cf222e',
  },
  dark: {
    background: '#111513',
    surface: '#181e1b',
    code: '#0b0f0d',
    codeText: '#e7f0ec',
    primary: '#68d4ad',
    primaryLight: '#91e0c3',
    primaryDark: '#2fab82',
    text: '#e3e3e3',
    textMuted: '#a9bbb3',
    border: '#3a4b43',
    warning: '#f2cc60',
    error: '#ff7b72',
  },
};

function createRedocOptions(colorMode) {
  const palette = redocPalettes[colorMode] ?? redocPalettes.dark;

  return {
    hideDownloadButtons: false,
    menuToggle: true,
    nativeScrollbars: true,
    schemasExpansionLevel: 1,
    scrollYOffset: () =>
      document.querySelector('.navbar')?.getBoundingClientRect().height ?? 0,
    showExtensions: true,
    sortOperationsAlphabetically: true,
    theme: {
      colors: {
        primary: {
          main: palette.primary,
          light: palette.primaryLight,
          dark: palette.primaryDark,
          contrastText: palette.background,
        },
        text: {primary: palette.text, secondary: palette.textMuted},
        border: {dark: palette.border, light: palette.border},
        success: {main: palette.primary},
        warning: {main: palette.warning},
        error: {main: palette.error},
      },
      sidebar: {
        backgroundColor: palette.background,
        textColor: palette.textMuted,
        activeTextColor: palette.primary,
      },
      rightPanel: {
        backgroundColor: palette.code,
        textColor: palette.codeText,
      },
      schema: {
        nestedBackground: palette.surface,
        linesColor: palette.border,
        typeNameColor: palette.primaryLight,
        typeTitleColor: palette.text,
        requireLabelColor: palette.error,
      },
      codeBlock: {backgroundColor: palette.code},
      typography: {
        fontFamily:
          'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
        headings: {
          fontFamily:
            'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
          fontWeight: '600',
        },
        heading1: {color: palette.text},
        heading2: {color: palette.text},
        heading3: {color: palette.text},
        code: {color: palette.primary, backgroundColor: palette.surface},
        links: {
          color: palette.primary,
          visited: palette.primary,
          hover: palette.primaryLight,
        },
      },
    },
  };
}

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

export default function EmbeddedRedoc() {
  const containerRef = useRef(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const {colorMode} = useColorMode();
  const specificationUrl = useBaseUrl('/openapi/swagger.json');
  const redocOptions = useMemo(
    () => createRedocOptions(colorMode),
    [colorMode],
  );

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
          redocOptions,
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
  }, [redocOptions, specificationUrl]);

  if (error) {
    return <div className="alert alert--danger">{error}</div>;
  }

  return (
    <div className="redoclyEmbeddedReference" data-redoc-theme={colorMode}>
      {loading && (
        <div className="redoclyLoading">Loading API documentation…</div>
      )}
      <div className="redoclyEmbeddedContent" ref={containerRef} />
    </div>
  );
}
